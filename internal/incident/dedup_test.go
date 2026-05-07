package incident

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func makeEv(fp string) IncidentEvent {
	return IncidentEvent{
		ID:          newID(),
		Fingerprint: fp,
		ReceivedAt:  time.Now(),
		Source:      SourceBrowserJS,
		Severity:    SeverityError,
		Category:    "TypeError",
		Summary:     "test error",
	}
}

func TestDedup_SameFingerprint_MergesCount(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(30 * time.Second)
	const n = 50
	ev := makeEv("fp-a")

	merged0, entry0 := d.Ingest("sess", ev)
	if merged0 {
		t.Fatal("first ingest must not be merged")
	}
	if entry0.Count != 1 {
		t.Fatalf("initial count: got %d, want 1", entry0.Count)
	}

	for i := 1; i < n; i++ {
		merged, e := d.Ingest("sess", makeEv("fp-a"))
		if !merged {
			t.Errorf("occurrence %d: expected merged=true", i+1)
		}
		if e.Count != i+1 {
			t.Errorf("occurrence %d: count=%d, want %d", i+1, e.Count, i+1)
		}
	}
	// Only one entry in the map.
	if d.Len() != 1 {
		t.Errorf("Len: got %d, want 1", d.Len())
	}
	_, final := d.Ingest("sess", makeEv("fp-a"))
	if final.Count != n+1 {
		t.Errorf("final count: got %d, want %d", final.Count, n+1)
	}
}

func TestDedup_DifferentFingerprints_Independent(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(30 * time.Second)
	for i := 0; i < 10; i++ {
		fp := fmt.Sprintf("fp-%d", i)
		merged, e := d.Ingest("sess", makeEv(fp))
		if merged {
			t.Errorf("fp %q: first occurrence must not merge", fp)
		}
		if e.Count != 1 {
			t.Errorf("fp %q: count=%d, want 1", fp, e.Count)
		}
	}
	if d.Len() != 10 {
		t.Errorf("Len: got %d, want 10", d.Len())
	}
}

func TestDedup_WindowExpiry_TreatsAsNew(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(50 * time.Millisecond) // short window for test
	merged1, e1 := d.Ingest("sess", makeEv("fp-x"))
	if merged1 || e1.Count != 1 {
		t.Fatal("first occurrence wrong")
	}
	time.Sleep(80 * time.Millisecond) // wait past window
	merged2, e2 := d.Ingest("sess", makeEv("fp-x"))
	if merged2 {
		t.Error("post-expiry: should be new entry, not merged")
	}
	if e2.Count != 1 {
		t.Errorf("post-expiry count: got %d, want 1", e2.Count)
	}
}

func TestDedup_SlidingWindow_ExtendedByActivity(t *testing.T) {
	t.Parallel()
	// Use 500ms window with 100ms sleep (5:1 ratio) so CI scheduler jitter
	// under -race load cannot push the sleep past the window expiry.
	d := NewDeduplicator(500 * time.Millisecond)
	d.Ingest("sess", makeEv("fp-slide"))
	// Each Ingest extends the window, so these should all merge.
	for i := 0; i < 3; i++ {
		time.Sleep(100 * time.Millisecond) // 100ms << 500ms window; window resets each time
		merged, _ := d.Ingest("sess", makeEv("fp-slide"))
		if !merged {
			t.Errorf("iteration %d: expected merged (window should be extended)", i)
		}
	}
}

func TestDedup_DifferentSessions_Independent(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(30 * time.Second)
	merged1, _ := d.Ingest("sess-A", makeEv("fp-shared"))
	merged2, _ := d.Ingest("sess-B", makeEv("fp-shared"))
	if merged1 || merged2 {
		t.Error("same fingerprint across different sessions must not merge")
	}
	if d.Len() != 2 {
		t.Errorf("Len: got %d, want 2", d.Len())
	}
}

func TestDedup_Trim_RemovesExpired(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(30 * time.Millisecond)
	d.Ingest("s", makeEv("exp1"))
	d.Ingest("s", makeEv("exp2"))
	d.Ingest("s", makeEv("exp3"))
	if d.Len() != 3 {
		t.Fatalf("before trim: len=%d, want 3", d.Len())
	}
	time.Sleep(50 * time.Millisecond)
	d.Trim()
	if d.Len() != 0 {
		t.Errorf("after trim: len=%d, want 0", d.Len())
	}
}

func TestDedup_FirstAndLastPreserved(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(30 * time.Second)
	first := makeEv("fp-fl")
	first.Summary = "first"
	d.Ingest("s", first)
	last := makeEv("fp-fl")
	last.Summary = "last"
	_, e := d.Ingest("s", last)
	if e.First.Summary != "first" {
		t.Errorf("First.Summary: got %q, want first", e.First.Summary)
	}
	if e.Last.Summary != "last" {
		t.Errorf("Last.Summary: got %q, want last", e.Last.Summary)
	}
}

func TestDedup_ConcurrentIngest_RaceClean(t *testing.T) {
	t.Parallel()
	d := NewDeduplicator(30 * time.Second)
	const goroutines = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				d.Ingest("sess", makeEv("shared-fp"))
			}
		}()
	}
	wg.Wait()
	_, e := d.Ingest("sess", makeEv("shared-fp"))
	// Must have count > 1 and exactly 1 entry.
	if e.Count < 2 {
		t.Errorf("count after concurrent ingests: %d, want >1", e.Count)
	}
	if d.Len() != 1 {
		t.Errorf("Len after concurrent: %d, want 1", d.Len())
	}
}
