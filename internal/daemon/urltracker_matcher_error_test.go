package daemon

import (
	"strings"
	"sync"
	"testing"
)

func TestURLTracker_InvalidMatcherIsReported(t *testing.T) {
	tracker := NewURLTracker(nil, DefaultURLTrackerConfig())
	var mu sync.Mutex
	var got []string
	tracker.onMatcherError = func(processID, msg string) {
		// The callback must run without the tracker lock held.
		tracker.mu.RLock()
		tracker.mu.RUnlock()
		mu.Lock()
		got = append(got, processID+": "+msg)
		mu.Unlock()
	}

	tracker.SetURLMatchers("p:dev", []string{"(unclosed"})

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want invalid-pattern and fallback reports, got %v", got)
	}
	if !strings.Contains(got[0], "invalid url-matcher") || !strings.Contains(got[1], "no usable url-matcher") {
		t.Fatalf("unexpected reports: %v", got)
	}
}

func TestURLTracker_ValidMatcherIsSilent(t *testing.T) {
	tracker := NewURLTracker(nil, DefaultURLTrackerConfig())
	tracker.onMatcherError = func(processID, msg string) { t.Errorf("unexpected report: %s", msg) }
	tracker.SetURLMatchers("p:dev", []string{`Local:\s*{url}`})
	tracker.SetURLMatchers("p:dev", nil)
}
