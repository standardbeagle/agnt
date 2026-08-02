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

func TestInbox_QueryAndMarkDoesNotLoseDuplicateArrivingBetweenPhases(t *testing.T) {
	inbox := NewInbox("sess")
	inbox.Ingest(makeEntry("fp", SeverityError))

	attempting := make(chan struct{})
	duplicateDone := make(chan struct{})
	var once sync.Once
	inbox.beforeIngestLock = func() { once.Do(func() { close(attempting) }) }

	entries, _ := inbox.QueryAndMark(QueryFilter{}, func(snapshot []InboxEntry) []string {
		go func() {
			inbox.Ingest(makeEntry("fp", SeverityError))
			close(duplicateDone)
		}()
		<-attempting // duplicate reached Ingest before the mark phase
		return []string{snapshot[0].Fingerprint}
	}, true)
	if len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("returned snapshot = %#v", entries)
	}
	<-duplicateDone

	unread, _ := inbox.Query(QueryFilter{UnreadOnly: true})
	if len(unread) != 1 || unread[0].Count != 2 {
		t.Fatalf("duplicate arriving after returned occurrence was marked got lost: %#v", unread)
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

// ── pin retention ─────────────────────────────────────────────────────────────

// TestInbox_PinnedEntrySurvivesBandEviction is the core of the retention
// feature: without the eviction exemption a pin is cosmetic, because the record
// the agent asked to keep is destroyed by ordinary traffic. Mutation check:
// delete the `if !...Pinned` skip in oldestUnpinned (or restore the plain
// lruList.Back() victim) and this test fails.
func TestInbox_PinnedEntrySurvivesBandEviction(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	inbox.Ingest(makeEntry("fp-keep", SeverityError))
	if _, err := inbox.Pin("fp-keep", "regression under investigation"); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// Push far past the band capacity with unpinned traffic.
	for i := 0; i < defaultBandCapacity*2; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-noise-%d", i), SeverityError))
	}

	kept := inbox.FindByFingerprint("fp-keep")
	if kept == nil {
		t.Fatal("pinned entry was evicted — the pin bought nothing")
	}
	if !kept.Pinned || kept.Tag != "regression under investigation" {
		t.Fatalf("pin state lost: pinned=%v tag=%q", kept.Pinned, kept.Tag)
	}
	// The exemption must not weaken the cap for unpinned entries: the earliest
	// noise entries are still gone, and the band still holds at most capacity.
	if got := inbox.FindByFingerprint("fp-noise-0"); got != nil {
		t.Error("unpinned entry survived overflow — the band cap was weakened")
	}
	if s := inbox.Stats(); s.Error > defaultBandCapacity {
		t.Errorf("error band holds %d entries, capacity is %d", s.Error, defaultBandCapacity)
	}
	if s := inbox.Stats(); s.Dropped == 0 {
		t.Error("expected unpinned evictions to be counted as drops")
	}
}

// TestInbox_UnpinRestoresEviction proves the exemption is scoped to the pin and
// released with it, rather than making an entry permanently immortal.
func TestInbox_UnpinRestoresEviction(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	inbox.Ingest(makeEntry("fp-keep", SeverityError))
	if _, err := inbox.Pin("fp-keep", "note"); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if !inbox.Unpin("fp-keep") {
		t.Fatal("Unpin reported no pin removed")
	}
	if got := inbox.PinnedCount(); got != 0 {
		t.Fatalf("PinnedCount=%d after unpin, want 0", got)
	}
	entry := inbox.FindByFingerprint("fp-keep")
	if entry == nil || entry.Pinned || entry.Tag != "" {
		t.Fatalf("unpin left state behind: %+v", entry)
	}

	for i := 0; i < defaultBandCapacity*2; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-noise-%d", i), SeverityError))
	}
	if inbox.FindByFingerprint("fp-keep") != nil {
		t.Error("unpinned entry still exempt from eviction")
	}
}

// TestInbox_PinSetIsBounded pins the memory-leak policy: the bound is enforced
// by REFUSING a new pin, never by discarding one the agent still holds.
func TestInbox_PinSetIsBounded(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	for i := 0; i < MaxPinnedEntries+1; i++ {
		inbox.Ingest(makeEntry(fmt.Sprintf("fp-%d", i), SeverityError))
	}
	for i := 0; i < MaxPinnedEntries; i++ {
		if _, err := inbox.Pin(fmt.Sprintf("fp-%d", i), ""); err != nil {
			t.Fatalf("Pin %d: %v", i, err)
		}
	}
	if _, err := inbox.Pin(fmt.Sprintf("fp-%d", MaxPinnedEntries), ""); err != ErrPinLimitReached {
		t.Fatalf("pin past the bound returned %v, want ErrPinLimitReached", err)
	}
	if got := inbox.PinnedCount(); got != MaxPinnedEntries {
		t.Fatalf("PinnedCount=%d, want %d", got, MaxPinnedEntries)
	}
	// The refusal must not have cost an existing pin.
	if e := inbox.FindByFingerprint("fp-0"); e == nil || !e.Pinned {
		t.Fatal("an existing pin was dropped to make room — pins must never be evicted")
	}
	// Re-pinning an existing pin updates the tag without consuming a slot, so a
	// retrying caller cannot exhaust the bound.
	if _, err := inbox.Pin("fp-0", "retagged"); err != nil {
		t.Fatalf("re-pin: %v", err)
	}
	if got := inbox.PinnedCount(); got != MaxPinnedEntries {
		t.Fatalf("re-pin changed PinnedCount to %d", got)
	}
	if e := inbox.FindByFingerprint("fp-0"); e == nil || e.Tag != "retagged" {
		t.Fatal("re-pin did not update the tag")
	}
	// Freeing a slot must make the bound usable again.
	inbox.Unpin("fp-0")
	if _, err := inbox.Pin(fmt.Sprintf("fp-%d", MaxPinnedEntries), ""); err != nil {
		t.Fatalf("pin after unpin: %v", err)
	}
}

// TestInbox_PinUnknownFingerprintFailsLoud — a pin whose target is already gone
// must report that, not report success and preserve nothing.
func TestInbox_PinUnknownFingerprintFailsLoud(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	if _, err := inbox.Pin("fp-absent", ""); err != ErrPinTargetNotFound {
		t.Fatalf("Pin of an absent fingerprint returned %v, want ErrPinTargetNotFound", err)
	}
	if inbox.Unpin("fp-absent") {
		t.Fatal("Unpin of an absent fingerprint reported success")
	}
}

// TestInbox_PinSurvivesRetentionClears covers the auto-retire triggers that
// already reach the inbox (build success / proc stop route through
// ClearProcessBefore; the agent's explicit clear routes through ClearAllBefore).
func TestInbox_PinSurvivesRetentionClears(t *testing.T) {
	t.Parallel()
	base := time.Now()

	withSample := func(fp, processID string) *InboxEntry {
		e := makeEntry(fp, SeverityError)
		e.LastSeenAt = base
		ev := NewIncidentEvent(SourceProcessAlert, SeverityError, "cat", "msg", Context{ProcessID: processID}, nil)
		e.Sample = &ev
		return e
	}

	t.Run("ClearAllBefore", func(t *testing.T) {
		t.Parallel()
		inbox := NewInbox("sess")
		inbox.Ingest(withSample("fp-keep", "web"))
		inbox.Ingest(withSample("fp-drop", "web"))
		if _, err := inbox.Pin("fp-keep", "keep me"); err != nil {
			t.Fatalf("Pin: %v", err)
		}
		if removed := inbox.ClearAllBefore(base.Add(time.Second)); removed != 1 {
			t.Fatalf("ClearAllBefore removed %d, want 1 (the unpinned entry only)", removed)
		}
		if inbox.FindByFingerprint("fp-keep") == nil {
			t.Error("pinned entry was cleared")
		}
		if inbox.FindByFingerprint("fp-drop") != nil {
			t.Error("unpinned entry survived the clear")
		}
	})

	t.Run("ClearProcessBefore", func(t *testing.T) {
		t.Parallel()
		inbox := NewInbox("sess")
		inbox.Ingest(withSample("fp-keep", "web"))
		inbox.Ingest(withSample("fp-drop", "web"))
		if _, err := inbox.Pin("fp-keep", "keep me"); err != nil {
			t.Fatalf("Pin: %v", err)
		}
		if removed := inbox.ClearProcessBefore("web", base.Add(time.Second)); removed != 1 {
			t.Fatalf("ClearProcessBefore removed %d, want 1", removed)
		}
		if inbox.FindByFingerprint("fp-keep") == nil {
			t.Error("pinned entry was retired by an auto-retire trigger")
		}
	})
}

// TestBus_PinIsSessionScoped enforces numbered contract 1 (per-session
// isolation, .claude/rules/daemon-architecture.md): retention is a per-session
// inbox operation, so a pin in session A must be invisible — and unreachable —
// from session B, even for the same fingerprint.
func TestBus_PinIsSessionScoped(t *testing.T) {
	t.Parallel()
	bus := NewMPSCBus(nil)
	defer bus.Close()

	bus.AddSession("sess-a", nil, nil, nil)
	bus.AddSession("sess-b", nil, nil, nil)

	plA := bus.getSessionPipeline("sess-a")
	plB := bus.getSessionPipeline("sess-b")
	plA.inbox.Ingest(makeEntry("fp-shared", SeverityError))
	plB.inbox.Ingest(makeEntry("fp-shared", SeverityError))

	if _, err := bus.PinSession("sess-a", "fp-shared", "a-only"); err != nil {
		t.Fatalf("PinSession(A): %v", err)
	}

	a := bus.FindFingerprintSession("sess-a", "fp-shared")
	if a == nil || !a.Pinned || a.Tag != "a-only" {
		t.Fatalf("session A does not see its own pin: %+v", a)
	}
	b := bus.FindFingerprintSession("sess-b", "fp-shared")
	if b == nil {
		t.Fatal("session B lost its own entry")
	}
	if b.Pinned || b.Tag != "" {
		t.Fatalf("session A's pin leaked into session B: pinned=%v tag=%q", b.Pinned, b.Tag)
	}
	if got := plB.inbox.PinnedCount(); got != 0 {
		t.Fatalf("session B reports %d pins, want 0", got)
	}
	// The write path is scoped too: B cannot release A's pin.
	if bus.UnpinSession("sess-b", "fp-shared") {
		t.Fatal("session B unpinned an entry it never pinned")
	}
	if a := bus.FindFingerprintSession("sess-a", "fp-shared"); a == nil || !a.Pinned {
		t.Fatal("session B's unpin reached session A's pin")
	}
	// An unknown session is a miss, never a fallback to some other inbox.
	if _, err := bus.PinSession("sess-ghost", "fp-shared", ""); err == nil {
		t.Fatal("PinSession on an unregistered session reported success")
	}
}
