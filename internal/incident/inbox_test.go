package incident

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func makeEntry(fp string, sev Severity) *InboxEntry {
	now := time.Now()
	return &InboxEntry{
		Fingerprint: fp,
		FirstSeenAt: now,
		LastSeenAt:  now,
		Count:       1,
		Sample:      nil,
		Severity:    sev,
	}
}

// ── bounded capacity ──────────────────────────────────────────────────────────

func TestInbox_BoundedCapacity_EvictsLRU(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	// Use error band (capacity=100).
	for i := 0; i < 100; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-%d", i), SeverityError))
	}
	s := inbox.Stats()
	if s.Error != 100 {
		t.Fatalf("pre-overflow: error band=%d, want 100", s.Error)
	}
	if s.Dropped != 0 {
		t.Fatalf("no drops expected yet, got %d", s.Dropped)
	}

	// 101st entry evicts the oldest.
	inbox.Ingest(makeEntry("fp-overflow", SeverityError))
	s = inbox.Stats()
	if s.Error != 100 {
		t.Errorf("post-overflow: error band=%d, want 100 (cap)", s.Error)
	}
	if s.Dropped != 1 {
		t.Errorf("dropped count: got %d, want 1", s.Dropped)
	}
}

func TestInbox_ManyEntries_DroppedCount(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	const total = 1000
	for i := 0; i < total; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-%d", i), SeverityError))
	}
	s := inbox.Stats()
	if s.Error != 100 {
		t.Errorf("band cap: got %d, want 100", s.Error)
	}
	if s.Dropped != total-100 {
		t.Errorf("dropped: got %d, want %d", s.Dropped, total-100)
	}
}

// ── severity escalation ───────────────────────────────────────────────────────

func TestInbox_SeverityEscalation_MovesBands(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	// 1. Insert as warning.
	warn := makeEntry("fp-esc", SeverityWarning)
	warn.Count = 3
	d1 := inbox.Ingest(warn)
	if !d1.IsNew {
		t.Fatal("first ingest must be new")
	}
	if inbox.Stats().Warning != 1 {
		t.Fatal("should be in warning band after first ingest")
	}

	// 2. Escalate to error.
	err := makeEntry("fp-esc", SeverityError)
	err.Count = 5
	d2 := inbox.Ingest(err)

	// 6 assertions per spec:
	// (a) not new (fingerprint-merge)
	if d2.IsNew {
		t.Error("(a) escalation should not be IsNew")
	}
	// (b) Escalated flag set
	if !d2.Escalated {
		t.Error("(b) Escalated should be true")
	}
	// (c) Count preserved (3 + 5 = 8)
	if d2.Entry.Count != 8 {
		t.Errorf("(c) Count: got %d, want 8", d2.Entry.Count)
	}
	// (d) LastSeenAt updated to newer entry's time
	if !d2.Entry.LastSeenAt.Equal(err.LastSeenAt) && !d2.Entry.LastSeenAt.After(warn.LastSeenAt) {
		t.Errorf("(d) LastSeenAt not updated: %v", d2.Entry.LastSeenAt)
	}
	// (e) Old band (warning) now empty
	if inbox.Stats().Warning != 0 {
		t.Errorf("(e) warning band should be empty after escalation, got %d", inbox.Stats().Warning)
	}
	// (f) New band (error) has the entry
	if inbox.Stats().Error != 1 {
		t.Errorf("(f) error band should have 1 entry, got %d", inbox.Stats().Error)
	}
}

// ── mark read & cursor ────────────────────────────────────────────────────────

func TestInbox_MarkRead_AdvancesCursor(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	inbox.Ingest(makeEntry("fp-a", SeverityError))
	inbox.Ingest(makeEntry("fp-b", SeverityError))

	if !inbox.Cursor().IsZero() {
		t.Fatal("cursor should start at zero")
	}
	inbox.MarkRead([]string{"fp-a", "fp-b"}, true)

	if inbox.Cursor().IsZero() {
		t.Error("cursor should advance after MarkRead with advanceCursor=true")
	}

	entries, _ := inbox.Query(QueryFilter{UnreadOnly: true})
	if len(entries) != 0 {
		t.Errorf("all entries marked read, unread query should return 0, got %d", len(entries))
	}
}

// ── query ─────────────────────────────────────────────────────────────────────

func TestInbox_Query_SinceCursor(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	// Insert two entries, record the time between them.
	inbox.Ingest(makeEntry("fp-old", SeverityError))
	checkpoint := time.Now()
	time.Sleep(time.Millisecond) // ensure strictly-after
	inbox.Ingest(makeEntry("fp-new", SeverityError))

	results, _ := inbox.Query(QueryFilter{Since: checkpoint})
	if len(results) != 1 {
		t.Errorf("since-filter: got %d results, want 1", len(results))
		return
	}
	if results[0].Fingerprint != "fp-new" {
		t.Errorf("since-filter: got fp=%q, want fp-new", results[0].Fingerprint)
	}
}

func TestInbox_Query_BandFilter_CriticalOnly(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	inbox.Ingest(makeEntry("fp-crit", SeverityCritical))
	inbox.Ingest(makeEntry("fp-err", SeverityError))
	inbox.Ingest(makeEntry("fp-warn", SeverityWarning))

	results, _ := inbox.Query(QueryFilter{Severities: []Severity{SeverityCritical}})
	if len(results) != 1 {
		t.Fatalf("critical-only filter: got %d results, want 1", len(results))
	}
	if results[0].Severity != SeverityCritical {
		t.Errorf("severity: got %q, want critical", results[0].Severity)
	}
}

func TestInbox_Query_Limit(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	for i := 0; i < 20; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-%d", i), SeverityError))
	}
	results, _ := inbox.Query(QueryFilter{Limit: 5})
	if len(results) != 5 {
		t.Errorf("limit: got %d results, want 5", len(results))
	}
}

func TestInbox_Query_NewestFirst(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	for i := 0; i < 5; i++ {
		e := makeEntry(fmt.Sprintf("fp-%d", i), SeverityError)
		e.LastSeenAt = time.Now().Add(time.Duration(i) * time.Millisecond)
		inbox.Ingest(e)
	}
	results, _ := inbox.Query(QueryFilter{})
	for i := 1; i < len(results); i++ {
		if results[i].LastSeenAt.After(results[i-1].LastSeenAt) {
			t.Errorf("results[%d] is newer than results[%d] — not sorted newest first", i, i-1)
		}
	}
}

// TestInbox_Query_CursorDrain_NoSkip pins the drain contract that the
// get_incidents tool advertises: pulling pages of `Limit`, advancing
// `Since` to each page's newest entry (the daemon handler's cursor =
// records[0].LastSeen), must eventually surface EVERY entry with no skips.
//
// Regression for the cursor-skip bug: when the inbox truncated to the
// newest `Limit` and the caller advanced the cursor past them, every
// entry older than the limit was skipped permanently. Returning the
// oldest unseen page instead lets the cursor sweep forward gap-free.
func TestInbox_Query_CursorDrain_NoSkip(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	const total = 12 // > Limit, spans more than two pages
	for i := 0; i < total; i++ {
		e := makeEntry(fmt.Sprintf("fp-%d", i), SeverityError)
		e.LastSeenAt = time.Now().Add(time.Duration(i) * time.Millisecond)
		inbox.Ingest(e)
	}

	seen := make(map[string]bool, total)
	var since time.Time
	for pulls := 0; pulls < total+5; pulls++ { // bounded to catch non-progress
		f := QueryFilter{Limit: 5}
		f.Since = since // zero on first pull = no lower bound
		page, _ := inbox.Query(f)
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			seen[e.Fingerprint] = true
		}
		// Cursor = newest entry of the page (page is newest-first → index 0),
		// mirroring buildIncidentQueryResult's cursor selection.
		since = page[0].LastSeenAt
	}

	if len(seen) != total {
		t.Fatalf("cursor drain skipped entries: saw %d of %d", len(seen), total)
	}
}

// ── stats ─────────────────────────────────────────────────────────────────────

func TestInbox_Stats_ReflectsState(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	inbox.Ingest(makeEntry("fp-c", SeverityCritical))
	inbox.Ingest(makeEntry("fp-e1", SeverityError))
	inbox.Ingest(makeEntry("fp-e2", SeverityError))
	inbox.Ingest(makeEntry("fp-w", SeverityWarning))
	inbox.Ingest(makeEntry("fp-i", SeverityInfo))

	s := inbox.Stats()
	if s.Critical != 1 || s.Error != 2 || s.Warning != 1 || s.Info != 1 {
		t.Errorf("stats: c=%d e=%d w=%d i=%d, want 1 2 1 1", s.Critical, s.Error, s.Warning, s.Info)
	}
	if s.Dropped != 0 {
		t.Errorf("dropped: got %d, want 0", s.Dropped)
	}
	if s.OldestUnread.IsZero() {
		t.Error("OldestUnread should be set when unread entries exist")
	}
}

// ── subscribe / delta ─────────────────────────────────────────────────────────

func TestInbox_Subscribe_Delta_NewVsMerge(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	ch, cancel := inbox.Subscribe()
	defer cancel()

	inbox.Ingest(makeEntry("fp-sub", SeverityError))
	d1 := <-ch
	if !d1.IsNew {
		t.Error("first ingest: IsNew should be true")
	}

	inbox.Ingest(makeEntry("fp-sub", SeverityError)) // merge
	d2 := <-ch
	if d2.IsNew {
		t.Error("second ingest: IsNew should be false (fingerprint-merge)")
	}
	if d2.Entry.Count != 2 {
		t.Errorf("merged count: got %d, want 2", d2.Entry.Count)
	}
}

func TestInbox_Subscribe_Cancel_Clears(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	_, cancel := inbox.Subscribe()
	cancel()
	// After cancel, Ingest must not panic/block (broadcast to closed channel handled).
	inbox.Ingest(makeEntry("fp-x", SeverityError))
}

// ── concurrent ────────────────────────────────────────────────────────────────

func TestInbox_ConcurrentIngestQuery(t *testing.T) {
	// No t.Parallel(): heavy goroutine count; let it run in isolation.
	inbox := NewInbox("sess")
	const goroutines = 100
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				fp := fmt.Sprintf("fp-%d-%d", id%10, j%5) // intentional overlap for merges
				inbox.Ingest(makeEntry(fp, SeverityError))
				if j%5 == 0 {
					inbox.Query(QueryFilter{Limit: 10})
				}
			}
		}(i)
	}
	wg.Wait()

	s := inbox.Stats()
	if s.Error > defaultBandCapacity {
		t.Errorf("error band overflow: %d > %d", s.Error, defaultBandCapacity)
	}
}

// ── read entries excluded from Stats ────────────────────────────────────────────

// Stats band counts and New reflect UNREAD entries only. Draining the inbox
// (marking entries read via a get_incidents pull) must drop the counts to zero
// so the pinger stops shouting error-level after the agent has seen everything.
func TestInbox_Stats_ExcludesReadEntries(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	inbox.Ingest(makeEntry("fp-c", SeverityCritical))
	inbox.Ingest(makeEntry("fp-e", SeverityError))
	inbox.Ingest(makeEntry("fp-w", SeverityWarning))

	s := inbox.Stats()
	if s.Critical != 1 || s.Error != 1 || s.Warning != 1 || s.New != 3 {
		t.Fatalf("before drain: c=%d e=%d w=%d new=%d, want 1 1 1 3", s.Critical, s.Error, s.Warning, s.New)
	}

	// Drain: mark every entry read.
	inbox.MarkRead([]string{"fp-c", "fp-e", "fp-w"}, true)

	s = inbox.Stats()
	if s.Critical != 0 || s.Error != 0 || s.Warning != 0 || s.Info != 0 || s.New != 0 {
		t.Errorf("after drain: c=%d e=%d w=%d i=%d new=%d, want all 0", s.Critical, s.Error, s.Warning, s.Info, s.New)
	}
	if !s.OldestUnread.IsZero() {
		t.Errorf("after drain: OldestUnread should be zero, got %v", s.OldestUnread)
	}
}
