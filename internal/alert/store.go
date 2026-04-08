// Package alert provides ring buffer storage and delivery routing for process alerts.
package alert

import (
	"sync"
	"time"
)

// AlertEntry represents a single alert stored in the daemon.
type AlertEntry struct {
	PatternID   string    `json:"pattern_id"`
	Severity    string    `json:"severity"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	Line        string    `json:"line"`
	ScriptID    string    `json:"script_id"`
	ProjectPath string    `json:"project_path,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// AlertStoreFilter is the daemon-side filter for querying alerts.
type AlertStoreFilter struct {
	Since       time.Time
	ProcessID   string
	Severity    string
	ProjectPath string
	Limit       int
}

// ProcessAlertStore is a ring buffer store for alert entries.
// head points to the oldest entry; new entries are written at (head+len) % maxSize.
type ProcessAlertStore struct {
	mu      sync.RWMutex
	entries []*AlertEntry
	head    int
	len     int
	maxSize int
}

// NewProcessAlertStore creates a new alert store with the given capacity.
func NewProcessAlertStore(maxSize int) *ProcessAlertStore {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &ProcessAlertStore{
		entries: make([]*AlertEntry, maxSize),
		maxSize: maxSize,
	}
}

// Add inserts an alert entry into the ring buffer.
func (s *ProcessAlertStore) Add(entry *AlertEntry) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	idx := (s.head + s.len) % s.maxSize
	s.entries[idx] = entry
	if s.len < s.maxSize {
		s.len++
	} else {
		// Buffer full: advance head (oldest entry overwritten)
		s.head = (s.head + 1) % s.maxSize
	}
	s.mu.Unlock()
}

// Query returns entries matching the filter, oldest-to-newest.
func (s *ProcessAlertStore) Query(filter AlertStoreFilter) []*AlertEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cap := s.len
	if filter.Limit > 0 && filter.Limit < cap {
		cap = filter.Limit
	}
	result := make([]*AlertEntry, 0, cap)
	for i := 0; i < s.len; i++ {
		entry := s.entries[(s.head+i)%s.maxSize]
		if entry == nil {
			continue
		}
		if !filter.Since.IsZero() && entry.Timestamp.Before(filter.Since) {
			continue
		}
		if filter.ProcessID != "" && entry.ScriptID != filter.ProcessID {
			continue
		}
		if filter.Severity != "" && entry.Severity != filter.Severity {
			continue
		}
		if filter.ProjectPath != "" && entry.ProjectPath != filter.ProjectPath {
			continue
		}
		result = append(result, entry)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result
}

// Clear resets the buffer.
func (s *ProcessAlertStore) Clear() {
	s.mu.Lock()
	for i := range s.entries {
		s.entries[i] = nil
	}
	s.head = 0
	s.len = 0
	s.mu.Unlock()
}

// ClearForProcess removes all alert entries for the given process ID.
// Returns the number of entries removed.
func (s *ProcessAlertStore) ClearForProcess(processID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var write int
	for i := 0; i < s.len; i++ {
		idx := (s.head + i) % s.maxSize
		if s.entries[idx] != nil && s.entries[idx].ScriptID == processID {
			s.entries[idx] = nil
			write++
		}
	}
	return write
}

// Len returns the current number of entries.
func (s *ProcessAlertStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.len
}
