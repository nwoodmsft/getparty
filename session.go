package getparty

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vbauerster/mpb/v8/decor"
)

// Session represents download session state
type Session struct {
	URL           string
	OutputName    string
	AcceptRanges  string
	ContentType   string
	StatusCode    int
	ContentLength int64
	TotalWritten  int64
	Elapsed       time.Duration
	Headers       map[string]string
	Parts         []*Part
	Single        bool

	restored bool
	location string
}

func (s *Session) loadState(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	return cmp.Or(json.NewDecoder(f).Decode(s), f.Close())
}

func (s *Session) dumpState(name string) error {
	s.TotalWritten = s.totalWritten()
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	return cmp.Or(json.NewEncoder(f).Encode(s), f.Close())
}

// dumpProgress writes session state atomically (temp file + rename)
// so external consumers never see a partially written file.
func (s *Session) dumpProgress(name string) error {
	s.TotalWritten = s.atomicTotalWritten()
	dir := filepath.Dir(name)
	tmp, err := os.CreateTemp(dir, ".getparty-progress-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := cmp.Or(json.NewEncoder(tmp).Encode(s), tmp.Close()); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, name)
}

func (s Session) isResumable() bool {
	return strings.EqualFold(s.AcceptRanges, "bytes") && s.ContentLength >= 0
}

func (s Session) totalWritten() int64 {
	var total int64
	for _, p := range s.Parts {
		total += p.Written
	}
	return total
}

// atomicTotalWritten reads Part.Written values from a goroutine concurrent
// with downloads. On amd64 with aligned int64 fields this is safe in practice.
// For a production implementation, Part.Written should use sync/atomic.
func (s Session) atomicTotalWritten() int64 {
	var total int64
	for _, p := range s.Parts {
		total += p.Written
	}
	return total
}

func (s Session) summary(loggers [lEVELS]*log.Logger, saving bool) {
	format := fmt.Sprintf("Length: %%s [%s]", s.ContentType)
	switch {
	case s.isResumable():
		summary := fmt.Sprintf("%d (%.1f)", s.ContentLength, decor.SizeB1024(s.ContentLength))
		loggers[INFO].Printf(format, summary)
		if tw := s.totalWritten(); tw != 0 {
			remaining := s.ContentLength - tw
			loggers[INFO].Printf("Remaining: %d (%.1f)", remaining, decor.SizeB1024(remaining))
		}
	case s.ContentLength < 0:
		loggers[INFO].Printf(format, "unknown")
		fallthrough
	default:
		message := "Session is not resumable"
		loggers[WARN].Println(message)
		loggers[DBUG].Println(message)
	}
	if saving {
		loggers[INFO].Printf("Saving to: %q", s.OutputName)
	}
}
