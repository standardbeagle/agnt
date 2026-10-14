package overlay

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The overlay's Enter retries ask whether a typed message is still in the
// agent's input box. The answer comes from the rows at the cursor, compared on
// letters and digits so borders and wrapping do not matter.
func TestActivityMonitor_CursorRowsContain(t *testing.T) {
	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:  time.Hour,
		OnOutputLine: func(string) {},
		InitialCols:  20,
		InitialRows:  10,
	})
	defer am.Stop()

	// An input box, the message wrapped across two rows, cursor at its end.
	_, _ = am.Write([]byte("history line\r\n╭──────────────────╮\r\n│ > check the bu   │\r\n│ ild please       │"))
	found, ok := am.CursorRowsContain("thebuildplease")
	if !ok || !found {
		t.Fatalf("a wrapped message in the input box: found=%v ok=%v, want true true", found, ok)
	}

	// Submitted: the box is redrawn empty.
	_, _ = am.Write([]byte("\x1b[2J\x1b[H╭──────────────────╮\r\n│ >                │\r\n╰──────────────────╯\x1b[2;5H"))
	if found, ok := am.CursorRowsContain("thebuildplease"); !ok || found {
		t.Fatalf("an emptied input box: found=%v ok=%v, want false true", found, ok)
	}

	if _, ok := am.CursorRowsContain("a-b"); ok {
		t.Error("a tail with too few letters and digits must be reported as unknown")
	}
}

// Preview snapshots reach the callback in order, newest last, even when the
// callback is slow: one goroutine per snapshot let an older one land after a
// newer one.
func TestActivityMonitor_PreviewSnapshotsStayInOrder(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []int
	first := true
	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:     time.Hour,
		ShowDoneMessage: false,
		PreviewDebounce: time.Millisecond,
		OnOutputPreview: func(lines []string, _ string) {
			mu.Lock()
			wait := first
			first = false
			mu.Unlock()
			if wait {
				<-release // a slow consumer
			}
			n := 0
			for _, l := range lines {
				var v int
				if _, err := fmt.Sscanf(l, "line%d", &v); err == nil && v > n {
					n = v
				}
			}
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
		},
	})
	defer am.Stop()

	for i := 1; i <= 6; i++ {
		_, _ = fmt.Fprintf(am, "line%d\r\n", i)
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0 && got[len(got)-1] == 6
	}, 5*time.Second, 5*time.Millisecond, "the newest snapshot must be delivered")
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("snapshots delivered out of order: %v", got)
		}
	}
}

// Without a preview or line callback the monitor keeps no screen, so it
// cannot answer.
func TestActivityMonitor_CursorRowsContainUnknownWithoutScreen(t *testing.T) {
	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{IdleTimeout: time.Hour})
	defer am.Stop()
	_, _ = am.Write([]byte("check the build"))
	if _, ok := am.CursorRowsContain("checkthebuild"); ok {
		t.Fatal("want ok=false when the screen is not fed")
	}
}
