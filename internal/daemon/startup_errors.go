package daemon

import (
	"sync"
	"time"
)

// StartupLogEntry records a startup event (success or failure) with diagnostic context.
type StartupLogEntry struct {
	ProcessID  string    `json:"process_id"`
	ScriptName string    `json:"script_name"` // Name without project prefix
	Level      string    `json:"level"`       // "info", "warning", "error"
	EventType  string    `json:"event_type"`  // "started", "EADDRINUSE", "start_failed", "cleanup_failed", "port_cleanup", "dependency_wait", "skipped"
	Message    string    `json:"message"`
	Output     string    `json:"output,omitempty"` // Last lines of process output (errors only)
	Port       int       `json:"port,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// StartupLogStore is a ring buffer of recent startup events.
// Survives process cleanup so logs remain queryable after processes
// are removed from the registry.
type StartupLogStore struct {
	mu      sync.RWMutex
	entries []*StartupLogEntry
	head    int
	len     int
	maxSize int
}

// NewStartupLogStore creates a new store with the given capacity.
func NewStartupLogStore(maxSize int) *StartupLogStore {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &StartupLogStore{
		entries: make([]*StartupLogEntry, maxSize),
		maxSize: maxSize,
	}
}

// Add inserts a startup log entry into the ring buffer.
func (s *StartupLogStore) Add(entry *StartupLogEntry) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := (s.head + s.len) % s.maxSize
	s.entries[idx] = entry
	if s.len < s.maxSize {
		s.len++
	} else {
		s.head = (s.head + 1) % s.maxSize
	}
}

// Info adds an informational log entry.
func (s *StartupLogStore) Info(processID, scriptName, eventType, message string) {
	s.Add(&StartupLogEntry{
		ProcessID:  processID,
		ScriptName: scriptName,
		Level:      "info",
		EventType:  eventType,
		Message:    message,
		Timestamp:  time.Now(),
	})
}

// Error adds an error log entry.
func (s *StartupLogStore) Error(processID, scriptName, eventType, message string) {
	s.Add(&StartupLogEntry{
		ProcessID:  processID,
		ScriptName: scriptName,
		Level:      "error",
		EventType:  eventType,
		Message:    message,
		Timestamp:  time.Now(),
	})
}

// StartupLogFilter controls which entries are returned by Query.
type StartupLogFilter struct {
	Since     time.Time
	ProcessID string
	Level     string // "info", "warning", "error", or "" for all
	Limit     int
}

// Query returns matching entries ordered oldest to newest.
func (s *StartupLogStore) Query(filter StartupLogFilter) []*StartupLogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*StartupLogEntry
	for i := 0; i < s.len; i++ {
		entry := s.entries[(s.head+i)%s.maxSize]
		if entry == nil {
			continue
		}
		if !filter.Since.IsZero() && entry.Timestamp.Before(filter.Since) {
			continue
		}
		if filter.ProcessID != "" && entry.ProcessID != filter.ProcessID {
			continue
		}
		if filter.Level != "" && entry.Level != filter.Level {
			continue
		}
		result = append(result, entry)
	}

	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[len(result)-filter.Limit:]
	}
	return result
}

// Recent returns entries from the last duration, up to limit.
func (s *StartupLogStore) Recent(d time.Duration, limit int) []*StartupLogEntry {
	return s.Query(StartupLogFilter{
		Since: time.Now().Add(-d),
		Limit: limit,
	})
}

// Clear removes all entries.
func (s *StartupLogStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = 0
	s.len = 0
}

// Len returns the current number of entries.
func (s *StartupLogStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.len
}
