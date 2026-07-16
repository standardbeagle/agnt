package incident

import (
	"sync"
	"time"
)

// DedupEntry tracks merged occurrences of a recurring incident fingerprint
// within a sliding deduplication window.
type DedupEntry struct {
	First     IncidentEvent
	Last      IncidentEvent
	Count     int
	expiresAt time.Time
}

// Deduplicator merges repeated IncidentEvents that share a fingerprint into
// one DedupEntry, incrementing Count on each duplicate. The window is sliding:
// each duplicate resets the expiry. A fingerprint seen after its window expires
// is treated as a new occurrence.
type Deduplicator struct {
	window  time.Duration
	mu      sync.Mutex
	entries map[string]*DedupEntry
}

// NewDeduplicator creates a Deduplicator with the given sliding window.
// Use 30s in production; tests can use shorter values.
func NewDeduplicator(window time.Duration) *Deduplicator {
	return &Deduplicator{
		window:  window,
		entries: make(map[string]*DedupEntry),
	}
}

// Ingest adds ev to the deduplicator. Returns (true, existing entry) when ev
// deduplicates against a live entry; returns (false, new entry) on first
// occurrence or after the window has expired.
func (d *Deduplicator) Ingest(sessionID string, ev IncidentEvent) (merged bool, entry *DedupEntry) {
	key := sessionID + "|" + ev.Fingerprint
	now := time.Now()
	ev = cloneIncidentEvent(ev)

	d.mu.Lock()
	defer d.mu.Unlock()

	if e, ok := d.entries[key]; ok && now.Before(e.expiresAt) {
		e.Count++
		e.Last = ev
		e.expiresAt = now.Add(d.window)
		return true, cloneDedupEntry(e)
	}

	e := &DedupEntry{
		First:     ev,
		Last:      ev,
		Count:     1,
		expiresAt: now.Add(d.window),
	}
	d.entries[key] = e
	return false, cloneDedupEntry(e)
}

// cloneDedupEntry returns an ownership snapshot while the deduplicator lock is
// held. Callers must never retain pointers into mutable entries in d.entries.
func cloneDedupEntry(entry *DedupEntry) *DedupEntry {
	if entry == nil {
		return nil
	}
	cloned := *entry
	cloned.First = cloneIncidentEvent(entry.First)
	cloned.Last = cloneIncidentEvent(entry.Last)
	return &cloned
}

func cloneIncidentEvent(event IncidentEvent) IncidentEvent {
	cloned := event
	if event.PayloadRef != nil {
		payload := *event.PayloadRef
		cloned.PayloadRef = &payload
	}
	cloned.Remediation.PrimaryArgs = cloneStringAnyMap(event.Remediation.PrimaryArgs)
	cloned.Remediation.FallbackArgs = cloneStringAnyMap(event.Remediation.FallbackArgs)
	return cloned
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneIncidentValue(value)
	}
	return cloned
}

// cloneIncidentValue covers the JSON-shaped values accepted in remediation
// arguments. Scalar values are immutable; maps and slices are recursively owned.
func cloneIncidentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i := range typed {
			cloned[i] = cloneIncidentValue(typed[i])
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, item := range typed {
			cloned[key] = item
		}
		return cloned
	default:
		return value
	}
}

// Trim removes expired entries, reclaiming memory.
func (d *Deduplicator) Trim() {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, e := range d.entries {
		if now.After(e.expiresAt) {
			delete(d.entries, k)
		}
	}
}

// Len returns the number of currently tracked fingerprints.
func (d *Deduplicator) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}
