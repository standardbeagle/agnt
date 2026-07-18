// Package alert provides ring buffer storage and delivery routing for process alerts.
package alert

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// AlertEntry represents a single alert stored in the daemon.
type AlertEntry struct {
	// ID is the stable unified-error id (8-char hex) stamped by the store at
	// Add time. It is the same id get_errors presents, so retention actions
	// (pin/unpin) can address an entry by the id the agent already saw.
	ID          string    `json:"id,omitempty"`
	PatternID   string    `json:"pattern_id"`
	Severity    string    `json:"severity"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	Line        string    `json:"line"`
	ScriptID    string    `json:"script_id"`
	ProjectPath string    `json:"project_path,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// UnifiedID computes the stable unified-error id for the entry — the same
// recipe get_errors uses (sha256("process:<script>"|CATEGORY|message|"")[:4]
// as hex), so the daemon and the MCP tool agree on ids without a round trip.
// The category/message derivation mirrors tools.alertMapToUnifiedError; a
// parity test in internal/tools pins the two together.
func (e *AlertEntry) UnifiedID() string {
	category := strings.ToUpper(e.Category)
	if category == "" {
		category = "PROCESS ERROR"
	}
	category = strings.ReplaceAll(category, "_", " ")

	message := e.Line
	if e.Category == "process_lifecycle" && e.Description != "" {
		if message == "" || message == e.Description {
			message = e.Description
		} else {
			message = e.Description + " — " + message
		}
	} else if message == "" {
		message = e.Description
	}

	h := sha256.New()
	h.Write([]byte("process:" + e.ScriptID))
	h.Write([]byte(category))
	h.Write([]byte(message))
	return hex.EncodeToString(h.Sum(nil)[:4])
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

// Add inserts an alert entry into the ring buffer, stamping its unified id.
func (s *ProcessAlertStore) Add(entry *AlertEntry) {
	if entry == nil {
		return
	}
	if entry.ID == "" {
		entry.ID = entry.UnifiedID()
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
	return s.clearWhere(func(e *AlertEntry) bool {
		return e.ScriptID == processID
	})
}

// ClearProcessBefore removes entries for processID whose Timestamp is at or
// before the given boundary. The boundary is what makes build-success clearing
// safe: an error line emitted after the success signal keeps a later timestamp
// and survives, regardless of when the clear actually executes.
func (s *ProcessAlertStore) ClearProcessBefore(processID string, before time.Time) int {
	return s.clearWhere(func(e *AlertEntry) bool {
		return e.ScriptID == processID && !e.Timestamp.After(before)
	})
}

// ClearProject removes all entries stamped with the given project path.
// Used when the last session for a project disconnects: the next session
// must not inherit a ring full of errors from processes that no longer run.
func (s *ProcessAlertStore) ClearProject(projectPath string) int {
	return s.clearWhere(func(e *AlertEntry) bool {
		return e.ProjectPath == projectPath
	})
}

// clearWhere compacts the ring, dropping entries matching pred while
// preserving order. It rewrites the ring from index 0 so head/len stay
// consistent (the previous nil-in-place implementation left len stale).
func (s *ProcessAlertStore) clearWhere(pred func(*AlertEntry) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]*AlertEntry, 0, s.len)
	removed := 0
	for i := 0; i < s.len; i++ {
		entry := s.entries[(s.head+i)%s.maxSize]
		if entry == nil {
			continue
		}
		if pred(entry) {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	if removed == 0 {
		return 0
	}
	for i := range s.entries {
		s.entries[i] = nil
	}
	copy(s.entries, kept)
	s.head = 0
	s.len = len(kept)
	return removed
}

// Len returns the current number of entries.
func (s *ProcessAlertStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.len
}
