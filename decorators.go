package getparty

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vbauerster/mpb/v4/decor"
)

type message struct {
	msg   string
	times int
	final bool
	done  chan struct{}
}

type msgGate struct {
	prefix string
	msgCh  chan *message
	done   chan struct{}
}

func newMsgGate(prefix string, quiet bool) msgGate {
	gate := msgGate{
		prefix: prefix,
		done:   make(chan struct{}),
	}
	if !quiet {
		gate.msgCh = make(chan *message, 4)
	}
	return gate
}

func (s msgGate) flash(msg *message) {
	msg.times = 14
	msg.msg = fmt.Sprintf("%s:%s", s.prefix, msg.msg)
	select {
	case s.msgCh <- msg:
	case <-s.done:
		if msg.final && msg.done != nil {
			close(msg.done)
		}
	}
}

type mainDecorator struct {
	decor.WC
	curTry   *uint32
	name     string
	format   string
	flashMsg *message
	messages []*message
	gate     msgGate
}

func newMainDecorator(curTry *uint32, format, name string, gate msgGate, wc decor.WC) decor.Decorator {
	d := &mainDecorator{
		WC:     wc.Init(),
		curTry: curTry,
		name:   name,
		format: format,
		gate:   gate,
	}
	return d
}

func (d *mainDecorator) depleteMessages() {
	for {
		select {
		case m := <-d.gate.msgCh:
			d.messages = append(d.messages, m)
		default:
			return
		}
	}
}

func (d *mainDecorator) Decor(stat *decor.Statistics) string {
	if !stat.Completed && d.flashMsg != nil {
		m := d.flashMsg.msg
		if d.flashMsg.times > 0 {
			d.flashMsg.times--
		} else if d.flashMsg.final {
			if d.flashMsg.done != nil {
				close(d.flashMsg.done)
				d.flashMsg.done = nil
			}
		} else {
			d.flashMsg = nil
		}
		return d.FormatMsg(m)
	}

	d.depleteMessages()

	if len(d.messages) > 0 {
		d.flashMsg = d.messages[0]
		copy(d.messages, d.messages[1:])
		d.messages = d.messages[:len(d.messages)-1]
		d.flashMsg.times--
		return d.FormatMsg(d.flashMsg.msg)
	}

	name := d.name
	if atomic.LoadUint32(&globTry) > 0 {
		name = fmt.Sprintf("%s:R%02d", name, atomic.LoadUint32(d.curTry))
	}
	return d.FormatMsg(fmt.Sprintf(d.format, name, decor.SizeB1024(stat.Total)))
}

func (d *mainDecorator) Shutdown() {
	close(d.gate.done)
}

type speedPeak struct {
	decor.WC
	format       string
	msg          string
	max          float64
	window       uint
	displayCount uint
	peak         struct {
		sync.Mutex
		sum   float64
		total int
	}
	once sync.Once
}

func newSpeedPeak(format string, wc decor.WC) decor.Decorator {
	d := &speedPeak{
		WC:     wc.Init(),
		format: format,
		window: 1200 / refreshRate,
	}
	return d
}

func (s *speedPeak) NextAmount(n int64, wdd ...time.Duration) {
	var workDuration time.Duration
	for _, wd := range wdd {
		workDuration = wd
	}
	durPerByte := float64(workDuration) / float64(n)
	if math.IsInf(durPerByte, 0) || math.IsNaN(durPerByte) {
		return
	}
	s.peak.Lock()
	s.peak.sum += durPerByte
	s.peak.total++
	s.peak.Unlock()
}

func (s *speedPeak) onComplete() {
	s.msg = fmt.Sprintf(
		s.format,
		decor.FmtAsSpeed(decor.SizeB1024(math.Round(s.max*1e9))),
	)
}

func (s *speedPeak) Decor(st *decor.Statistics) string {
	if st.Completed {
		s.once.Do(s.onComplete)
	} else if s.displayCount != 0 && s.displayCount%s.window == 0 {
		s.peak.Lock()
		if s.peak.total > 0 {
			v := s.peak.sum / float64(s.peak.total)
			s.peak.sum = 0
			s.peak.total = 0
			s.peak.Unlock()
			s.max = math.Max(s.max, 1/v)
		} else {
			s.peak.Unlock()
		}
	}
	s.displayCount++
	return s.FormatMsg(s.msg)
}
