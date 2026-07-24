package incident

import (
	"container/list"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

const (
	defaultBandCapacity = 100
	numBands            = 4
	subChanBuf          = 16
	maxSampleURLs       = 10  // distinct sample URLs surfaced on a storm entry
	maxDistinctURLs     = 128 // hard cap so a flood cannot grow the set unbounded
)

// InboxEntry is a deduplicated, severity-banded record of an incident fingerprint.
type InboxEntry struct {
	Fingerprint  string         `json:"fingerprint"`
	FirstSeenAt  time.Time      `json:"first_seen_at"`
	LastSeenAt   time.Time      `json:"last_seen_at"`
	Count        int            `json:"count"`
	Sample       *IncidentEvent `json:"sample,omitempty"`
	SampleURLs   []string       `json:"sample_urls,omitempty"`   // up to maxSampleURLs distinct
	DistinctURLs int            `json:"distinct_urls,omitempty"` // distinct URLs seen, capped
	Severity     Severity       `json:"severity"`
	Read         bool           `json:"read"`

	urlSeen map[string]struct{} // unexported: distinct-URL set, capped at maxDistinctURLs
}

// InboxDelta is delivered to Subscribe subscribers on each Ingest.
type InboxDelta struct {
	Entry     *InboxEntry
	IsNew     bool // false = fingerprint-merge
	Escalated bool // severity moved entry to a higher band
}

// Stats is a point-in-time snapshot of inbox health. The per-band counts and
// New reflect UNREAD entries only: once an entry is marked read (the agent
// pulled it via get_incidents), it no longer inflates the counts or the ping's
// severity level, so pings stop shouting error-level after the inbox is drained.
type Stats struct {
	Critical     int       `json:"critical"`
	Error        int       `json:"error"`
	Warning      int       `json:"warning"`
	Info         int       `json:"info"`
	New          int       `json:"new"` // total unread entries across all bands
	Dropped      int64     `json:"dropped"`
	OldestUnread time.Time `json:"oldest_unread,omitempty"`
	Cursor       time.Time `json:"cursor,omitempty"`
}

// QueryFilter narrows which entries Query returns.
type QueryFilter struct {
	Severities []Severity // empty = all
	Since      time.Time  // zero = no lower bound
	// SinceFingerprint completes the stable (LastSeenAt, Fingerprint) cursor
	// tuple. HasSinceFingerprint=false preserves legacy timestamp-only behavior.
	SinceFingerprint    string
	HasSinceFingerprint bool
	UnreadOnly          bool
	Limit               int // 0 = all
}

type inboxSlot struct {
	entry *InboxEntry
	elem  *list.Element // position in band's lruList
}

type band struct {
	severity Severity
	capacity int
	mu       sync.RWMutex
	slots    map[string]*inboxSlot
	lruList  *list.List // back = least recently updated
}

func newBand(sev Severity, capacity int) *band {
	return &band{
		severity: sev,
		capacity: capacity,
		slots:    make(map[string]*inboxSlot),
		lruList:  list.New(),
	}
}

// Inbox is a bounded per-session incident inbox with 4 severity bands
// (critical, error, warning, info). Concurrent-safe.
type Inbox struct {
	SessionID string

	bands   [numBands]*band
	dropped atomic.Int64

	// ingestMu serializes the whole find-then-insert sequence of Ingest. Each
	// band has its own lock, but a fingerprint's presence is checked band by
	// band with the lock released between bands, so two concurrent Ingests
	// could both miss an existing entry and double-insert, or race an escalation
	// move. This inbox-level lock makes the
	// scan+insert atomic without widening any band lock's scope.
	ingestMu sync.Mutex

	cursorMu sync.Mutex
	cursor   time.Time

	subsMu sync.Mutex
	subs   []chan InboxDelta

	beforeIngestLock func() // test-only synchronization hook
}

// NewInbox creates a session inbox with default 100-entry band capacities.
func NewInbox(sessionID string) *Inbox {
	inbox := &Inbox{SessionID: sessionID}
	for i, sev := range []Severity{SeverityCritical, SeverityError, SeverityWarning, SeverityInfo} {
		inbox.bands[i] = newBand(sev, defaultBandCapacity)
	}
	return inbox
}

func bandIndex(sev Severity) int {
	switch sev {
	case SeverityCritical:
		return 0
	case SeverityError:
		return 1
	case SeverityWarning:
		return 2
	default:
		return 3
	}
}

// addSampleURL records url against entry for storm rendering. It keeps up to
// maxSampleURLs distinct sample strings and counts distinct URLs up to
// maxDistinctURLs so memory stays bounded under a flood. Empty urls are ignored.
// Callers must hold the owning band's lock (or own the entry pre-insert).
func addSampleURL(entry *InboxEntry, url string) {
	if url == "" {
		return
	}
	if entry.urlSeen == nil {
		entry.urlSeen = make(map[string]struct{})
	}
	if _, ok := entry.urlSeen[url]; ok {
		return
	}
	if len(entry.urlSeen) >= maxDistinctURLs {
		return
	}
	entry.urlSeen[url] = struct{}{}
	entry.DistinctURLs = len(entry.urlSeen)
	if len(entry.SampleURLs) < maxSampleURLs {
		entry.SampleURLs = append(entry.SampleURLs, url)
	}
}

func snapshotInboxEntry(entry *InboxEntry) *InboxEntry {
	if entry == nil {
		return nil
	}
	snapshot := *entry
	snapshot.SampleURLs = append([]string(nil), entry.SampleURLs...)
	if entry.urlSeen != nil {
		snapshot.urlSeen = make(map[string]struct{}, len(entry.urlSeen))
		for url := range entry.urlSeen {
			snapshot.urlSeen[url] = struct{}{}
		}
	}
	if entry.Sample != nil {
		sample := cloneIncidentEvent(*entry.Sample)
		snapshot.Sample = &sample
	}
	return &snapshot
}

// Ingest adds or merges an InboxEntry into the appropriate severity band.
// If the fingerprint already exists in any band, Count is incremented and
// LastSeenAt updated; severity escalation moves the entry to a higher band.
// Returns the resulting InboxDelta.
func (inbox *Inbox) Ingest(entry *InboxEntry) InboxDelta {
	if inbox.beforeIngestLock != nil {
		inbox.beforeIngestLock()
	}
	inbox.ingestMu.Lock()
	defer inbox.ingestMu.Unlock()

	newIdx := bandIndex(entry.Severity)

	for i := 0; i < numBands; i++ {
		b := inbox.bands[i]
		b.mu.Lock()
		slot, ok := b.slots[entry.Fingerprint]
		if !ok {
			b.mu.Unlock()
			continue
		}

		existing := slot.entry
		existing.Count += entry.Count
		// A duplicate is a new unread occurrence even when earlier occurrences of
		// this fingerprint were acknowledged.
		existing.Read = false
		if entry.Sample != nil {
			addSampleURL(existing, entry.Sample.Ctx.URL)
		}
		if entry.LastSeenAt.After(existing.LastSeenAt) {
			existing.LastSeenAt = entry.LastSeenAt
			existing.Sample = entry.Sample
		}

		escalated := newIdx < i
		var snapshot *InboxEntry
		if escalated {
			existing.Severity = entry.Severity
			snapshot = snapshotInboxEntry(existing)
			b.lruList.Remove(slot.elem)
			delete(b.slots, entry.Fingerprint)
			b.mu.Unlock()
			inbox.insertIntoBand(inbox.bands[newIdx], existing)
		} else {
			b.lruList.MoveToFront(slot.elem)
			snapshot = snapshotInboxEntry(existing)
			b.mu.Unlock()
		}

		delta := InboxDelta{Entry: snapshot, IsNew: false, Escalated: escalated}
		inbox.broadcast(delta)
		return delta
	}

	// First occurrence of this fingerprint.
	if entry.Sample != nil {
		addSampleURL(entry, entry.Sample.Ctx.URL)
	}
	snapshot := snapshotInboxEntry(entry)
	inbox.insertIntoBand(inbox.bands[newIdx], entry)
	delta := InboxDelta{Entry: snapshot, IsNew: true}
	inbox.broadcast(delta)
	return delta
}

func (inbox *Inbox) insertIntoBand(b *band, entry *InboxEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for len(b.slots) >= b.capacity {
		back := b.lruList.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*inboxSlot)
		b.lruList.Remove(back)
		delete(b.slots, evicted.entry.Fingerprint)
		inbox.dropped.Add(1)
	}

	slot := &inboxSlot{entry: entry}
	slot.elem = b.lruList.PushFront(slot)
	b.slots[entry.Fingerprint] = slot
}

// Query returns entries matching filter. Results are sorted newest first.
func (inbox *Inbox) Query(filter QueryFilter) ([]InboxEntry, Stats) {
	// Keep a fingerprint's band membership and severity atomic with escalation.
	// Ingest takes this lock before any band lock, so Query follows the same
	// order to avoid observing the remove/reinsert transition.
	inbox.ingestMu.Lock()
	defer inbox.ingestMu.Unlock()
	return inbox.queryLocked(filter)
}

func (inbox *Inbox) queryLocked(filter QueryFilter) ([]InboxEntry, Stats) {
	var results []InboxEntry
	for _, b := range inbox.bands {
		if len(filter.Severities) > 0 && !containsSeverity(filter.Severities, b.severity) {
			continue
		}
		b.mu.RLock()
		for _, slot := range b.slots {
			e := slot.entry
			if filter.UnreadOnly && e.Read {
				continue
			}
			if !filter.Since.IsZero() {
				after := e.LastSeenAt.After(filter.Since)
				if filter.HasSinceFingerprint && e.LastSeenAt.Equal(filter.Since) {
					after = e.Fingerprint > filter.SinceFingerprint
				}
				if !after {
					continue
				}
			}
			results = append(results, *snapshotInboxEntry(e))
		}
		b.mu.RUnlock()
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].LastSeenAt.Equal(results[j].LastSeenAt) {
			return results[i].Fingerprint > results[j].Fingerprint
		}
		return results[i].LastSeenAt.After(results[j].LastSeenAt)
	})
	// When truncating, keep the OLDEST `Limit` matched entries (the tail of the
	// newest-first slice), not the newest. With a forward-only `Since` cursor,
	// returning the newest page and advancing the cursor past it permanently
	// skips the older unread tail. Returning the oldest unseen page and letting
	// the caller advance the cursor to that page's newest entry lets successive
	// `since=cursor` pulls sweep the whole inbox with no gaps. Entries inside the
	// page stay newest-first for display.
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[len(results)-filter.Limit:]
	}
	return results, inbox.Stats()
}

// QueryAndMark atomically snapshots a query, lets the caller select which of
// those exact snapshots were returned, and marks only that selection read.
// Ingest cannot merge a duplicate between the query and mark phases.
func (inbox *Inbox) QueryAndMark(filter QueryFilter, selectFingerprints func([]InboxEntry) []string, advanceCursor bool) ([]InboxEntry, Stats) {
	inbox.ingestMu.Lock()
	defer inbox.ingestMu.Unlock()
	entries, stats := inbox.queryLocked(filter)
	inbox.markReadLocked(selectFingerprints(entries), advanceCursor)
	return entries, stats
}

// MarkRead marks entries as read and optionally advances the cursor.
func (inbox *Inbox) MarkRead(fingerprints []string, advanceCursor bool) {
	inbox.ingestMu.Lock()
	defer inbox.ingestMu.Unlock()
	inbox.markReadLocked(fingerprints, advanceCursor)
}

func (inbox *Inbox) markReadLocked(fingerprints []string, advanceCursor bool) {
	fpSet := make(map[string]bool, len(fingerprints))
	for _, fp := range fingerprints {
		fpSet[fp] = true
	}

	var maxSeen time.Time
	for _, b := range inbox.bands {
		b.mu.Lock()
		for fp, slot := range b.slots {
			if fpSet[fp] {
				slot.entry.Read = true
				if slot.entry.LastSeenAt.After(maxSeen) {
					maxSeen = slot.entry.LastSeenAt
				}
			}
		}
		b.mu.Unlock()
	}

	if advanceCursor && !maxSeen.IsZero() {
		inbox.cursorMu.Lock()
		if maxSeen.After(inbox.cursor) {
			inbox.cursor = maxSeen
		}
		inbox.cursorMu.Unlock()
	}
}

// Cursor returns the last acknowledged position (time of last MarkRead).
func (inbox *Inbox) Cursor() time.Time {
	inbox.cursorMu.Lock()
	defer inbox.cursorMu.Unlock()
	return inbox.cursor
}

// Stats returns a snapshot of band counts and inbox health.
func (inbox *Inbox) Stats() Stats {
	s := Stats{Dropped: inbox.dropped.Load()}
	inbox.cursorMu.Lock()
	s.Cursor = inbox.cursor
	inbox.cursorMu.Unlock()

	for i, b := range inbox.bands {
		b.mu.RLock()
		unread := 0
		for _, slot := range b.slots {
			if slot.entry.Read {
				continue
			}
			unread++
			t := slot.entry.LastSeenAt
			if s.OldestUnread.IsZero() || t.Before(s.OldestUnread) {
				s.OldestUnread = t
			}
		}
		b.mu.RUnlock()
		switch i {
		case 0:
			s.Critical = unread
		case 1:
			s.Error = unread
		case 2:
			s.Warning = unread
		case 3:
			s.Info = unread
		}
		s.New += unread
	}
	return s
}

// Subscribe returns a buffered channel delivering InboxDelta on each Ingest.
// Slow consumers have deltas dropped (logged via debug, not silent). Call cancel when done.
func (inbox *Inbox) Subscribe() (<-chan InboxDelta, func()) {
	ch := make(chan InboxDelta, subChanBuf)
	inbox.subsMu.Lock()
	inbox.subs = append(inbox.subs, ch)
	inbox.subsMu.Unlock()

	cancel := func() {
		inbox.subsMu.Lock()
		defer inbox.subsMu.Unlock()
		for i, c := range inbox.subs {
			if c == ch {
				inbox.subs = append(inbox.subs[:i], inbox.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, cancel
}

func (inbox *Inbox) broadcast(delta InboxDelta) {
	inbox.subsMu.Lock()
	defer inbox.subsMu.Unlock()
	for _, ch := range inbox.subs {
		select {
		case ch <- delta:
		default:
			// Contract #7: delivery never blocks. A slow consumer drops this
			// delta by design — the Pinger re-reads inbox state on its own
			// cadence, so a dropped delta only delays a ping. Log for diagnosis.
			debug.Log("incident-inbox", "subscriber slow; dropped inbox delta")
		}
	}
}

// ClearProcessBefore removes entries attributed to processID whose LastSeenAt
// is at or before the boundary. Entries that recurred after the boundary keep
// a later LastSeenAt and survive — the error is still happening, so retiring
// it would be lying to the agent. Returns the number of entries removed.
func (inbox *Inbox) ClearProcessBefore(processID string, before time.Time) int {
	if processID == "" {
		return 0
	}
	removed := 0
	for _, b := range inbox.bands {
		b.mu.Lock()
		for fp, slot := range b.slots {
			e := slot.entry
			if e.Sample == nil || e.Sample.Ctx.ProcessID != processID {
				continue
			}
			if e.LastSeenAt.After(before) {
				continue
			}
			b.lruList.Remove(slot.elem)
			delete(b.slots, fp)
			removed++
		}
		b.mu.Unlock()
	}
	return removed
}

// ClearAllBefore removes every entry whose LastSeenAt is at or before the
// boundary, regardless of source. Backs the agent's explicit project-wide
// "clear": entries that recur after the boundary re-insert on their next
// occurrence. Returns the number of entries removed.
func (inbox *Inbox) ClearAllBefore(before time.Time) int {
	removed := 0
	for _, b := range inbox.bands {
		b.mu.Lock()
		for fp, slot := range b.slots {
			if slot.entry.LastSeenAt.After(before) {
				continue
			}
			b.lruList.Remove(slot.elem)
			delete(b.slots, fp)
			removed++
		}
		b.mu.Unlock()
	}
	return removed
}

// FindByFingerprint returns a deep snapshot of the entry with the given
// fingerprint, or nil when no band holds it. A shallow copy would share
// Sample/SampleURLs/urlSeen with the live entry that Ingest mutates under
// the band lock — a data race for any reader. ingestMu is taken so the
// result cannot transiently miss an entry mid-escalation.
func (inbox *Inbox) FindByFingerprint(fp string) *InboxEntry {
	inbox.ingestMu.Lock()
	defer inbox.ingestMu.Unlock()
	for _, b := range inbox.bands {
		b.mu.RLock()
		if slot, ok := b.slots[fp]; ok {
			e := snapshotInboxEntry(slot.entry)
			b.mu.RUnlock()
			return e
		}
		b.mu.RUnlock()
	}
	return nil
}

func containsSeverity(sevs []Severity, sev Severity) bool {
	for _, s := range sevs {
		if s == sev {
			return true
		}
	}
	return false
}
