package getparty

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/vbauerster/backoff"
	"github.com/vbauerster/backoff/exponential"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const (
	bufSize = 4096
)

var globTry uint32

// Part represents state of each download part
type Part struct {
	FileName string
	Start    int64
	Stop     int64
	Written  int64
	Skip     bool
	Elapsed  time.Duration

	name      string
	order     int
	maxTry    int
	quiet     bool
	jar       http.CookieJar
	totalBar  *mpb.Bar
	transport *http.Transport
	dlogger   *log.Logger
}

func (p Part) makeBar(progress *mpb.Progress, curTry *uint32) (*mpb.Bar, *msgGate) {
	total := p.total()
	if total < 0 {
		total = 0
	}
	p.dlogger.Printf("Bar total: %d", total)
	mg := newMsgGate(p.quiet, p.name, 15)
	bar := progress.New(total,
		mpb.BarFillerBuilderFunc(func() mpb.BarFiller {
			if total == 0 {
				return mpb.NopStyle().Build()
			}
			return mpb.BarStyle().Lbound(" ").Rbound(" ").Build()
		}),
		mpb.BarFillerTrim(),
		mpb.BarPriority(p.order),
		mpb.PrependDecorators(
			newMainDecorator(curTry, "%s %.1f", p.name, mg, decor.WCSyncWidthR),
			decor.Conditional(
				total == 0,
				decor.OnComplete(decor.Spinner([]string{`-`, `\`, `|`, `/`}, decor.WCSyncSpace), "100% "),
				decor.OnComplete(decor.NewPercentage("%.2f", decor.WCSyncSpace), "100%"),
			),
		),
		mpb.AppendDecorators(
			decor.OnComplete(
				decor.Conditional(
					total == 0,
					decor.Name(""),
					decor.OnAbort(
						decor.NewAverageETA(
							decor.ET_STYLE_MMSS,
							time.Now(),
							decor.FixedIntervalTimeNormalizer(30),
							decor.WCSyncWidth,
						), "--:--"),
				), "Avg:"),
			decor.AverageSpeed(decor.UnitKiB, "%.1f", decor.WCSyncSpace),
			decor.OnComplete(decor.Name("", decor.WCSyncSpace), "Peak:"),
			newSpeedPeak("%.1f", decor.WCSyncSpace),
		),
	)
	return bar, mg
}

func (p *Part) download(
	ctx context.Context,
	progress *mpb.Progress,
	req *http.Request,
	timeout uint,
) (err error) {
	fpart, err := os.OpenFile(p.FileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return errors.Wrap(err, p.name)
	}
	defer func() {
		if e := fpart.Close(); err == nil {
			err = e
		}
		if p.Skip && err == nil {
			p.dlogger.Printf("Removing: %q", fpart.Name())
			err = os.Remove(p.FileName)
		}
		err = errors.Wrap(err, p.name)
	}()

	var bar *mpb.Bar
	var mg *msgGate
	var curTry uint32
	var ranDur time.Duration
	resetDur := time.Duration(timeout*2) * time.Second
	barInitDone := make(chan struct{})
	prefix := p.dlogger.Prefix()
	initialTimeout := timeout
	start := time.Now()

	return backoff.Retry(ctx,
		exponential.New(
			exponential.WithReset(resetDur),
			exponential.WithBaseDelay(500*time.Millisecond),
		),
		func(attempt int) (retry bool, err error) {
			pw := p.Written
			defer func() {
				if err != nil {
					p.dlogger.Printf("ERR: retry quit: %s", err.Error())
				}
				ranDur = time.Since(start)
				if pw != p.Written {
					p.Elapsed += ranDur
				}
				p.dlogger.Printf("Ran dur: %v", ranDur)
				start = time.Now()
			}()

			req.Header.Set(hRange, p.getRange())

			p.dlogger.SetPrefix(fmt.Sprintf("%s[%02d] ", prefix, attempt))
			p.dlogger.Printf("GET %q", req.URL)
			p.dlogger.Printf("%s: %s", hUserAgentKey, req.Header.Get(hUserAgentKey))
			p.dlogger.Printf("%s: %s", hRange, req.Header.Get(hRange))

			if attempt > 0 {
				if ranDur < resetDur {
					timeout += 5
				} else {
					timeout = initialTimeout
				}
				atomic.AddUint32(&globTry, 1)
				atomic.StoreUint32(&curTry, uint32(attempt))
			}

			ctxTimeout := time.Duration(timeout) * time.Second
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			timer := time.AfterFunc(ctxTimeout, func() {
				cancel()
				// checking for mg != nil here is a data race
				select {
				case <-barInitDone:
					mg.flash("Timeout...")
				default:
				}
				p.dlogger.Printf("Timeout after: %v", ctxTimeout)
			})
			defer timer.Stop()

			client := &http.Client{
				Transport: p.transport,
				Jar:       p.jar,
			}
			resp, err := client.Do(req.WithContext(ctx))
			if err != nil {
				if attempt+1 == p.maxTry {
					if bar != nil {
						mg.finalFlash(ErrMaxRetry.Error())
						bar.Abort(false)
					}
					return false, errors.WithMessage(ErrMaxRetry, err.Error())
				}
				return true, err
			}

			p.dlogger.Printf("Status: %s", resp.Status)
			p.dlogger.Printf("ContentLength: %d", resp.ContentLength)
			if cookies := p.jar.Cookies(req.URL); len(cookies) != 0 {
				p.dlogger.Println("CookieJar:")
				for _, cookie := range cookies {
					p.dlogger.Printf("  %q", cookie)
				}
			}

			switch resp.StatusCode {
			case http.StatusOK: // no partial content, so download with single part
				if p.order != 1 {
					p.Skip = true
					p.dlogger.Print("Skip: no partial content")
					return false, nil
				}
				if attempt == 0 && p.totalBar != nil {
					p.totalBar.Abort(true)
				}
				if resp.ContentLength > 0 {
					p.Stop = resp.ContentLength - 1
				}
				p.Written = 0
			case http.StatusForbidden, http.StatusTooManyRequests:
				if mg != nil {
					mg.finalFlash(resp.Status)
				}
				fallthrough
			default:
				if resp.StatusCode != http.StatusPartialContent {
					return false, &HttpError{resp.StatusCode, resp.Status}
				}
			}

			if bar == nil {
				bar, mg = p.makeBar(progress, &curTry)
				close(barInitDone)
			}

			body := bar.ProxyReader(resp.Body)
			defer body.Close()

			if p.Written > 0 {
				bar.SetRefill(p.Written)
				p.dlogger.Printf("Bar refill: %d", p.Written)
				if attempt == 0 {
					bar.SetCurrent(p.Written)
					bar.DecoratorAverageAdjust(time.Now().Add(-p.Elapsed))
				}
			}

			buf := make([]byte, bufSize)
			for n := 0; err == nil && timer.Reset(ctxTimeout); {
				n, err = io.ReadFull(body, buf)
				if err != nil {
					if err == io.ErrUnexpectedEOF {
						p.dlogger.Printf("io.ReadFull: %d %s", n, err.Error())
						if n > 0 {
							err = nil
						}
					} else {
						p.dlogger.Printf("Timer stop: %v", timer.Stop())
					}
				}
				n, e := fpart.Write(buf[:n])
				if e != nil {
					p.dlogger.Printf("ERR: write to %q: %s", fpart.Name(), err.Error())
					panic(err)
				}
				if p.totalBar != nil {
					p.totalBar.IncrBy(n)
				}
				p.Written += int64(n)
				if p.total() <= 0 {
					bar.SetTotal(p.Written, err == io.EOF)
				}
			}

			p.dlogger.Printf("Written to %q: %d", fpart.Name(), p.Written-pw)

			if err == io.EOF {
				p.dlogger.Println(err.Error())
				if p.isDone() || p.total() <= 0 {
					return false, nil
				} else {
					panic("written != total after EOF")
				}
			}

			if attempt+1 == p.maxTry {
				mg.finalFlash(ErrMaxRetry.Error())
				bar.Abort(false)
				return false, ErrMaxRetry
			}

			if resp.StatusCode != http.StatusPartialContent {
				bar.SetCurrent(0)
				bar.SetTotal(0, false)
				e := fpart.Close()
				if e != nil {
					p.dlogger.Printf("ERR: fpart.Close: %s", e.Error())
					panic(e)
				}
				fpart, e = os.OpenFile(p.FileName, os.O_WRONLY|os.O_TRUNC, 0644)
				if e != nil {
					p.dlogger.Printf("ERR: os.OpenFile: %s", e.Error())
					panic(e)
				}
			}

			return true, err
		})
}

func (p Part) getRange() string {
	if p.Stop < 1 {
		return "bytes=0-"
	}
	return fmt.Sprintf("bytes=%d-%d", p.Start+p.Written, p.Stop)
}

func (p Part) isDone() bool {
	return p.Written > 0 && p.Written == p.total()
}

func (p Part) total() int64 {
	return p.Stop - p.Start + 1
}
