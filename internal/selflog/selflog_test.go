package selflog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")

	RecordTo(path, "hook", "daemon enqueue deadline exceeded")
	RecordTo(path, "pinger", "mcp ping delivery failed: broken pipe")

	entries, err := Read(path, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].Component != "hook" || !strings.Contains(entries[0].Message, "deadline exceeded") {
		t.Fatalf("entry0 mismatch: %+v", entries[0])
	}
	if entries[1].Component != "pinger" || !strings.Contains(entries[1].Message, "broken pipe") {
		t.Fatalf("entry1 mismatch: %+v", entries[1])
	}
	if entries[0].Time.IsZero() {
		t.Fatalf("timestamp not parsed: %+v", entries[0])
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	entries, err := Read(filepath.Join(t.TempDir(), "nope.log"), 0)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if entries != nil {
		t.Fatalf("want nil, got %v", entries)
	}
}

func TestTailLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	for i := 0; i < 10; i++ {
		RecordTo(path, "hook", "event")
	}
	entries, err := Read(path, 3)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("tail want 3, got %d", len(entries))
	}
}

func TestCountSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	RecordTo(path, "hook", "old")
	// All records are stamped at write time (now), so a cutoff in the
	// future counts zero and a cutoff in the past counts all.
	if n, _ := CountSince(path, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("future cutoff want 0, got %d", n)
	}
	if n, _ := CountSince(path, time.Now().Add(-time.Hour)); n != 1 {
		t.Fatalf("past cutoff want 1, got %d", n)
	}
}

func TestClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	RecordTo(path, "hook", "x")
	if err := Clear(path); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	entries, _ := Read(path, 0)
	if len(entries) != 0 {
		t.Fatalf("want empty after clear, got %d", len(entries))
	}
	// Clear on already-absent file is not an error.
	if err := Clear(path); err != nil {
		t.Fatalf("Clear on missing: %v", err)
	}
}

func TestMultilineMessageStaysOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	RecordTo(path, "hook", "line one\nline two\nline three")
	entries, err := Read(path, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("multiline must collapse to 1 record, got %d", len(entries))
	}
	if strings.Contains(entries[0].Message, "\n") {
		t.Fatalf("message still has newline: %q", entries[0].Message)
	}
}

func TestComponentWithSpacesSanitized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	RecordTo(path, "post tool use", "msg")
	entries, _ := Read(path, 0)
	if len(entries) != 1 || strings.Contains(entries[0].Component, " ") {
		t.Fatalf("component not sanitized: %+v", entries)
	}
}
