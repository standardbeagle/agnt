package incident

import (
	"container/list"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultBandCapacity = 100
	numBands            = 4
	staleReadAge        = 5 * time.Minute
	subChanBuf          = 16
)

// InboxEntry is a deduplicated, severity-banded record of an incident fingerprint.
type InboxEntry struct {
	Fingerprint string         `json:"fingerprint"`
	FirstSeenAt time.Time      `json:"first_seen_at"`
	LastSeenAt  time.Time      `json:"last_seen_at"`
	Count       int            `json:"count"`
	Sample      *IncidentEvent `json:"sample,omitempty"`
	Severity    Severity       `json:"severity"`
	Read        bool           `json:"read"`
}

// InboxDelta is delivered to Subscribe subscribers on each Ingest.
type InboxDelta struct {
	Entry     *InboxEntry
	IsNew     bool // false = fingerprint-merge
	Escalated bool // severity moved entry to a higher band
}

// Stats is a point-in-time snapshot of inbox health.
type Stats struct {
	Critical     int       `json:"critical"`
	Error        int       `json:"error"`
	Warning      int       `json:"warning"`
	Info         int       `json:"info"`
	Dropped      int64     `json:"dropped"`
	OldestUnread time.Time `json:"oldest_unread,omitempty"`
	Cursor       time.Time `json:"cursor,omitempty"`
}

// QueryFilter narrows which entries Query returns.
type QueryFilter struct {
	Severities []Severity // empty = all
	Since      time.Time  // zero = no lower bound
	UnreadOnly bool
	Limit      int // 0 = all
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

	cursorMu sync.Mutex
	cursor   time.Time

	subsMu sync.Mutex
	subs   []chan InboxDelta
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

// Ingest adds or merges an InboxEntry into the appropriate severity band.
// If the fingerprint already exists in any band, Count is incremented and
// LastSeenAt updated; severity escalation moves the entry to a higher band.
// Returns the resulting InboxDelta.
func (inbox *Inbox) Ingest(entry *InboxEntry) InboxDelta {
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
		if entry.LastSeenAt.After(existing.LastSeenAt) {
			existing.LastSeenAt = entry.LastSeenAt
			existing.Sample = entry.Sample
		}

		escalated := newIdx < i
		if escalated {
			b.lruList.Remove(slot.elem)
			delete(b.slots, entry.Fingerprint)
			b.mu.Unlock()
			existing.Severity = entry.Severity
			inbox.insertIntoBand(inbox.bands[newIdx], existing)
		} else {
			b.lruList.MoveToFront(slot.elem)
			b.mu.Unlock()
		}

		delta := InboxDelta{Entry: existing, IsNew: false, Escalated: escalated}
		inbox.broadcast(delta)
		return delta
	}

	// First occurrence of this fingerprint.
	inbox.insertIntoBand(inbox.bands[newIdx], entry)
	delta := InboxDelta{Entry: entry, IsNew: true}
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
			if !filter.Since.IsZero() && !e.LastSeenAt.After(filter.Since) {
				continue
			}
			results = append(results, *e)
		}
		b.mu.RUnlock()
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].LastSeenAt.After(results[j].LastSeenAt)
	})
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[:filter.Limit]
	}
	return results, inbox.Stats()
}

// MarkRead marks entries as read and optionally advances the cursor.
func (inbox *Inbox) MarkRead(fingerprints []string, advanceCursor bool) {
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
		n := len(b.slots)
		for _, slot := range b.slots {
			if !slot.entry.Read {
				t := slot.entry.LastSeenAt
				if s.OldestUnread.IsZero() || t.Before(s.OldestUnread) {
					s.OldestUnread = t
				}
			}
		}
		b.mu.RUnlock()
		switch i {
		case 0:
			s.Critical = n
		case 1:
			s.Error = n
		case 2:
			s.Warning = n
		case 3:
			s.Info = n
		}
	}
	return s
}

// Subscribe returns a buffered channel delivering InboxDelta on each Ingest.
// Slow consumers receive dropped silently. Call cancel when done.
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
		default: // slow consumer: drop
		}
	}
}

// GC removes Read entries older than staleReadAge from all bands.
func (inbox *Inbox) GC() {
	cutoff := time.Now().Add(-staleReadAge)
	for _, b := range inbox.bands {
		b.mu.Lock()
		for fp, slot := range b.slots {
			if slot.entry.Read && slot.entry.LastSeenAt.Before(cutoff) {
				b.lruList.Remove(slot.elem)
				delete(b.slots, fp)
			}
		}
		b.mu.Unlock()
	}
}

// SaveCritical persists unread critical entries to path as JSON.
func (inbox *Inbox) SaveCritical(path string) error {
	b := inbox.bands[0]
	b.mu.RLock()
	entries := make([]*InboxEntry, 0, len(b.slots))
	for _, slot := range b.slots {
		if !slot.entry.Read {
			e := *slot.entry
			entries = append(entries, &e)
		}
	}
	b.mu.RUnlock()

	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// LoadCritical loads previously saved critical entries. Silently ignores
// missing file (first session).
func (inbox *Inbox) LoadCritical(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries []*InboxEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	for _, e := range entries {
		inbox.Ingest(e)
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
