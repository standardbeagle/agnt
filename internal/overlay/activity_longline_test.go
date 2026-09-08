package overlay

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The per-line tap feeds alert matching, and what it is handed has to be a
// line the child actually printed. A line longer than the accumulator's cap
// used to reset the buffer and carry on, so the tail of that one logical line
// arrived at the tap as if it were a whole line of its own.
//
// Two things follow, and both reach the developer. The start of the line --
// where an error names itself -- never reaches the matcher, so a genuine long
// error is missed. And an arbitrary mid-line tail is matched on its own, so a
// substring inside a long JSON blob or a minified stack becomes an alert that
// is batched and typed into the agent.
func TestLongLineIsTruncated_NotSplitIntoASecondLine(t *testing.T) {
	var mu sync.Mutex
	var lines []string

	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:    time.Hour,
		MinActiveBytes: 1,
		OnOutputLine: func(line string) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, line)
		},
	})
	defer am.Stop()

	// One logical line: a long payload whose tail happens to read like a
	// failure, followed by the real end of the line.
	const filler = "payload"
	long := "GET /api/items 200 " + strings.Repeat(filler, 1200) + " error: connection refused\n"
	if len(long) <= 4096 {
		t.Fatalf("test line is %d bytes, not longer than the cap", len(long))
	}
	// A PTY hands output over in chunks, not one write per line.
	writeInChunks(am, long, 256)

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("one printed line produced %d tapped lines: %q", len(got), got)
	}
	if !strings.HasPrefix(got[0], "GET /api/items 200 ") {
		t.Errorf("the tapped line does not start where the printed line did: %.60q", got[0])
	}
	if strings.Contains(got[0], "error: connection refused") {
		t.Errorf("the tapped line carries text from beyond the cap: %.80q", got[0])
	}
}

// The next line must be unaffected: dropping the overflow ends with the line
// that overflowed, not with the one after it.
func TestLineAfterAnOverlongLineIsIntact(t *testing.T) {
	var mu sync.Mutex
	var lines []string

	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:    time.Hour,
		MinActiveBytes: 1,
		OnOutputLine: func(line string) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, line)
		},
	})
	defer am.Stop()

	writeInChunks(am, strings.Repeat("x", 9000)+"\nerror: build failed\n", 256)

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("want two tapped lines, got %d: %q", len(got), got)
	}
	if got[1] != "error: build failed" {
		t.Errorf("the line after the overlong one = %q, want %q", got[1], "error: build failed")
	}
}

// writeInChunks feeds s to the monitor in reads of at most n bytes, which is
// how a PTY delivers a long line: the cap is reached partway through, not at
// a line boundary.
func writeInChunks(am *ActivityMonitor, s string, n int) {
	for len(s) > 0 {
		size := n
		if size > len(s) {
			size = len(s)
		}
		am.captureForPreview([]byte(s[:size]))
		s = s[size:]
	}
}
