package getparty

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMakeParts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		length int64
		parts  [][2]int64
		err    error
	}{
		{
			name:   "0_33",
			length: 33,
			err:    ErrZeroParts,
		},
		{
			name:   "1_0",
			length: 0,
			parts:  [][2]int64{{0, -1}},
		},
		{
			name:   "1_-1",
			length: -1,
			parts:  [][2]int64{{0, -2}},
		},
		{
			name:   "1_1",
			length: 1,
			parts:  [][2]int64{{0, 0}},
		},
		{
			name:   "1_512",
			length: 512,
			parts:  [][2]int64{{0, 511}},
		},
		{
			name:   "1_512",
			length: 512,
			parts:  make([][2]int64, 2),
			err:    ErrTooFragmented,
		},
		{
			name:   "1_1024",
			length: 1024,
			parts:  [][2]int64{{0, 1023}},
		},
		{
			name:   "2_1024",
			length: 1024,
			parts: [][2]int64{
				{0, 511},
				{512, 1023},
			},
		},
		{
			name:   "3_1024",
			length: 1024,
			parts:  make([][2]int64, 3),
			err:    ErrTooFragmented,
		},
		{
			name:   "2_1025",
			length: 1025,
			parts: [][2]int64{
				{0, 511},
				{512, 1024},
			},
		},
		{
			name:   "2_2048",
			length: 2048,
			parts: [][2]int64{
				{0, 1023},
				{1024, 2047},
			},
		},
		{
			name:   "3_2048",
			length: 2048,
			parts: [][2]int64{
				{0, 681},
				{682, 1363},
				{1364, 2047},
			},
		},
		{
			name:   "4_2048",
			length: 2048,
			parts: [][2]int64{
				{0, 511},
				{512, 1023},
				{1024, 1535},
				{1536, 2047},
			},
		},
		{
			name:   "5_2048",
			length: 2048,
			parts:  make([][2]int64, 5),
			err:    ErrTooFragmented,
		},
		{
			name:   "4_2049",
			length: 2049,
			parts: [][2]int64{
				{0, 511},
				{512, 1023},
				{1024, 1535},
				{1536, 2048},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parts, err := makeParts(uint(len(test.parts)), test.length)
			if test.err != nil {
				if !errors.Is(err, test.err) {
					t.Errorf("expected error %q got %q", test.err, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %q", err)
				}
				if len(parts) != len(test.parts) {
					t.Errorf("expected len(parts)=%d got len(parts)=%d", len(test.parts), len(parts))
				}
				for i, p := range parts {
					x := test.parts[i]
					if start := x[0]; p.Start != start {
						t.Errorf("[%d] expected start %d got %d", i, start, p.Start)
					}
					if stop := x[1]; p.Stop != stop {
						t.Errorf("[%d] expected stop %d got %d", i, stop, p.Stop)
					}
				}
			}
		})
	}
}

func TestParseContentDisposition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input  string
		output string
	}{
		{
			input:  "",
			output: "",
		},
		{
			input:  "garbage",
			output: "",
		},
		{
			input:  "attachment; filename=",
			output: "",
		},
		{
			input:  "attachment; filename=''",
			output: "",
		},
		{
			input:  `attachment; filename=""`,
			output: "",
		},
		{
			input:  "attachment; garbage=filename",
			output: "",
		},
		{
			input:  "attachment; filename=filename",
			output: "filename",
		},
		{
			input:  "attachment; filename=content.txt",
			output: "content.txt",
		},
		{
			input:  "attachment; filename='content.txt'",
			output: "content.txt",
		},
		{
			input:  `attachment; filename="content.txt"`,
			output: "content.txt",
		},
		{
			input:  "attachment; filename*=UTF-8''content.txt",
			output: "content.txt",
		},
		{
			input:  "attachment; filename*=utf-8''%e2%82%ac%20rates",
			output: "€ rates",
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			output := parseContentDisposition(test.input)
			if output != test.output {
				t.Errorf("expected %q got %q", test.output, output)
			}
		})
	}
}

func TestDumpProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := filepath.Join(dir, "test-output.json")

	session := &Session{
		URL:           "https://example.com/file.iso",
		OutputName:    "file.iso",
		AcceptRanges:  "bytes",
		ContentType:   "application/octet-stream",
		StatusCode:    206,
		ContentLength: 10000,
		Parts: []*Part{
			{Start: 0, Stop: 4999, Written: 2500},
			{Start: 5000, Stop: 9999, Written: 1500},
		},
	}

	if err := session.dumpProgress(progressFile); err != nil {
		t.Fatalf("dumpProgress failed: %v", err)
	}

	data, err := os.ReadFile(progressFile)
	if err != nil {
		t.Fatalf("failed to read progress file: %v", err)
	}

	var loaded Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to unmarshal progress file: %v", err)
	}

	if loaded.TotalWritten != 4000 {
		t.Errorf("expected TotalWritten=4000, got %d", loaded.TotalWritten)
	}
	if loaded.ContentLength != 10000 {
		t.Errorf("expected ContentLength=10000, got %d", loaded.ContentLength)
	}
	if len(loaded.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(loaded.Parts))
	}
	if loaded.Parts[0].Written != 2500 {
		t.Errorf("expected Part[0].Written=2500, got %d", loaded.Parts[0].Written)
	}
	if loaded.Parts[1].Written != 1500 {
		t.Errorf("expected Part[1].Written=1500, got %d", loaded.Parts[1].Written)
	}
}

func TestDumpProgressAtomicWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	progressFile := filepath.Join(dir, "test-output.json")

	session := &Session{
		URL:           "https://example.com/file.iso",
		OutputName:    "file.iso",
		ContentLength: 5000,
		Parts:         []*Part{{Start: 0, Stop: 4999, Written: 1000}},
	}

	if err := session.dumpProgress(progressFile); err != nil {
		t.Fatalf("dumpProgress failed: %v", err)
	}

	// Verify no temp files left behind
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "test-output.json" {
			t.Errorf("unexpected file in dir: %s", e.Name())
		}
	}

	// Update and write again — should overwrite atomically
	session.Parts[0].Written = 3000
	if err := session.dumpProgress(progressFile); err != nil {
		t.Fatalf("dumpProgress (update) failed: %v", err)
	}

	var loaded Session
	data, _ := os.ReadFile(progressFile)
	json.Unmarshal(data, &loaded)
	if loaded.TotalWritten != 3000 {
		t.Errorf("expected TotalWritten=3000 after update, got %d", loaded.TotalWritten)
	}
}

func TestDumpStateIncludesTotalWritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "test-output.json")

	session := &Session{
		URL:           "https://example.com/file.iso",
		OutputName:    "file.iso",
		ContentLength: 8000,
		Parts: []*Part{
			{Start: 0, Stop: 3999, Written: 4000},
			{Start: 4000, Stop: 7999, Written: 4000},
		},
	}

	if err := session.dumpState(stateFile); err != nil {
		t.Fatalf("dumpState failed: %v", err)
	}

	data, _ := os.ReadFile(stateFile)
	var loaded Session
	json.Unmarshal(data, &loaded)
	if loaded.TotalWritten != 8000 {
		t.Errorf("expected TotalWritten=8000 from dumpState, got %d", loaded.TotalWritten)
	}
}
