// Package daemon provides the background daemon for persistent state management.
package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SessionStatus represents the current state of a session.
type SessionStatus string

const (
	// SessionStatusActive indicates the session is running and responsive.
	SessionStatusActive SessionStatus = "active"
	// SessionStatusDisconnected indicates the session has not sent a heartbeat recently.
	SessionStatusDisconnected SessionStatus = "disconnected"
)

// Session represents an active agnt run instance.
type Session struct {
	Code        string        `json:"code"`         // Unique session identifier (e.g., "claude-1", "dev")
	OverlayPath string        `json:"overlay_path"` // Unix socket path for overlay
	ProjectPath string        `json:"project_path"` // Directory where session was started
	Command     string        `json:"command"`      // Command being run (e.g., "claude")
	Args        []string      `json:"args"`         // Command arguments
	StartedAt   time.Time     `json:"started_at"`   // When session started
	Status      SessionStatus `json:"status"`       // Current status
	LastSeen    time.Time     `json:"last_seen"`    // Last heartbeat timestamp

	// Internal fields (not serialized)
	mu sync.RWMutex
}

// UpdateLastSeen updates the last seen timestamp and sets status to active.
func (s *Session) UpdateLastSeen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSeen = time.Now()
	s.Status = SessionStatusActive
}

// SetStatus updates the session status.
func (s *Session) SetStatus(status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

// GetStatus returns the current session status.
func (s *Session) GetStatus() SessionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// IsActive returns true if the session is currently active.
func (s *Session) IsActive() bool {
	return s.GetStatus() == SessionStatusActive
}

// ToJSON returns the session as a JSON-serializable map.
func (s *Session) ToJSON() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]interface{}{
		"code":         s.Code,
		"overlay_path": s.OverlayPath,
		"project_path": s.ProjectPath,
		"command":      s.Command,
		"args":         s.Args,
		"started_at":   s.StartedAt.Format(time.RFC3339),
		"status":       string(s.Status),
		"last_seen":    s.LastSeen.Format(time.RFC3339),
	}
}

// SessionRegistry manages active sessions with lock-free operations.
type SessionRegistry struct {
	sessions sync.Map // map[string]*Session

	// Statistics (atomics for lock-free access)
	totalRegistered   atomic.Int64
	totalUnregistered atomic.Int64
	activeCount       atomic.Int64

	// Heartbeat timeout configuration
	heartbeatTimeout time.Duration
}

// NewSessionRegistry creates a new session registry.
func NewSessionRegistry(heartbeatTimeout time.Duration) *SessionRegistry {
	if heartbeatTimeout == 0 {
		heartbeatTimeout = 60 * time.Second // Default 60 second timeout
	}
	return &SessionRegistry{
		heartbeatTimeout: heartbeatTimeout,
	}
}

// Register adds a new session to the registry.
func (r *SessionRegistry) Register(session *Session) error {
	if session.Code == "" {
		return fmt.Errorf("session code is required")
	}

	// Check if session already exists
	if _, loaded := r.sessions.LoadOrStore(session.Code, session); loaded {
		return fmt.Errorf("session %q already exists", session.Code)
	}

	r.totalRegistered.Add(1)
	r.activeCount.Add(1)
	return nil
}

// Unregister removes a session from the registry.
func (r *SessionRegistry) Unregister(code string) error {
	if _, loaded := r.sessions.LoadAndDelete(code); !loaded {
		return fmt.Errorf("session %q not found", code)
	}

	r.totalUnregistered.Add(1)
	r.activeCount.Add(-1)
	return nil
}

// Get retrieves a session by code.
func (r *SessionRegistry) Get(code string) (*Session, bool) {
	val, ok := r.sessions.Load(code)
	if !ok {
		return nil, false
	}
	return val.(*Session), true
}

// Heartbeat updates the last seen time for a session.
func (r *SessionRegistry) Heartbeat(code string) error {
	session, ok := r.Get(code)
	if !ok {
		return fmt.Errorf("session %q not found", code)
	}
	session.UpdateLastSeen()
	return nil
}

// List returns all sessions, optionally filtered by project path.
func (r *SessionRegistry) List(projectPath string, global bool) []*Session {
	var result []*Session
	r.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		// Filter by project path unless global is true
		if global || projectPath == "" || session.ProjectPath == projectPath {
			result = append(result, session)
		}
		return true
	})
	return result
}

// ListActive returns only active sessions.
func (r *SessionRegistry) ListActive(projectPath string, global bool) []*Session {
	var result []*Session
	r.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		if session.IsActive() {
			if global || projectPath == "" || session.ProjectPath == projectPath {
				result = append(result, session)
			}
		}
		return true
	})
	return result
}

// CheckHeartbeats marks sessions as disconnected if they haven't sent a heartbeat recently.
func (r *SessionRegistry) CheckHeartbeats() {
	cutoff := time.Now().Add(-r.heartbeatTimeout)
	r.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		session.mu.Lock()
		if session.Status == SessionStatusActive && session.LastSeen.Before(cutoff) {
			session.Status = SessionStatusDisconnected
			r.activeCount.Add(-1)
		}
		session.mu.Unlock()
		return true
	})
}

// ActiveCount returns the number of active sessions.
func (r *SessionRegistry) ActiveCount() int64 {
	return r.activeCount.Load()
}

// TotalRegistered returns the total number of sessions registered.
func (r *SessionRegistry) TotalRegistered() int64 {
	return r.totalRegistered.Load()
}

// TotalUnregistered returns the total number of sessions unregistered.
func (r *SessionRegistry) TotalUnregistered() int64 {
	return r.totalUnregistered.Load()
}

// GenerateSessionCode generates a unique session code based on command name.
// Returns codes like "claude-1", "claude-2", etc.
func (r *SessionRegistry) GenerateSessionCode(command string) string {
	// Find the next available number for this command prefix
	maxNum := 0
	prefix := command + "-"

	r.sessions.Range(func(key, value interface{}) bool {
		code := key.(string)
		if len(code) > len(prefix) && code[:len(prefix)] == prefix {
			var num int
			if _, err := fmt.Sscanf(code[len(prefix):], "%d", &num); err == nil {
				if num > maxNum {
					maxNum = num
				}
			}
		}
		return true
	})

	return fmt.Sprintf("%s-%d", command, maxNum+1)
}

// SessionInfo contains statistics about the session registry.
type SessionInfo struct {
	ActiveCount       int64 `json:"active_count"`
	TotalRegistered   int64 `json:"total_registered"`
	TotalUnregistered int64 `json:"total_unregistered"`
}

// Info returns statistics about the session registry.
func (r *SessionRegistry) Info() SessionInfo {
	return SessionInfo{
		ActiveCount:       r.activeCount.Load(),
		TotalRegistered:   r.totalRegistered.Load(),
		TotalUnregistered: r.totalUnregistered.Load(),
	}
}

// FindByDirectory finds an active session whose project path matches the given directory
// or any of its parent directories. Returns the most specific (deepest) match.
// This enables auto-attach behavior where MCP clients in subdirectories can find
// sessions started in parent directories.
func (r *SessionRegistry) FindByDirectory(directory string) (*Session, bool) {
	if directory == "" {
		return nil, false
	}

	// Normalize the search directory
	normalizedDir := normalizeSessionPath(directory)

	var bestMatch *Session
	var bestMatchDepth int = -1

	r.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)

		// Only consider active sessions
		if !session.IsActive() {
			return true
		}

		sessionPath := normalizeSessionPath(session.ProjectPath)
		if sessionPath == "" {
			return true
		}

		// Check if the session's project path is a prefix of (or equal to) the search directory
		// This means the session was started at the search directory or a parent
		if isPathPrefixOf(sessionPath, normalizedDir) {
			// Calculate depth (number of path components) to find the most specific match
			depth := strings.Count(sessionPath, string(filepath.Separator))
			if depth > bestMatchDepth {
				bestMatch = session
				bestMatchDepth = depth
			}
		}

		return true
	})

	return bestMatch, bestMatch != nil
}

// normalizeSessionPath normalizes a path for consistent comparison.
func normalizeSessionPath(path string) string {
	if path == "" || path == "." {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	// On Windows, normalize to lowercase for case-insensitive comparison
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

// isPathPrefixOf checks if prefix is a path prefix of target.
// For example: "/home/user/project" is a prefix of "/home/user/project/subdir"
func isPathPrefixOf(prefix, target string) bool {
	if prefix == target {
		return true
	}
	// Ensure we match on path boundaries, not just string prefixes
	// "/home/user/proj" should NOT match "/home/user/project"
	if !strings.HasPrefix(target, prefix) {
		return false
	}
	// Check that the character after prefix is a path separator
	if len(target) > len(prefix) {
		nextChar := target[len(prefix)]
		return nextChar == filepath.Separator || nextChar == '/'
	}
	return false
}

// MarshalJSON implements json.Marshaler for Session.
func (s *Session) MarshalJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type sessionJSON struct {
		Code        string   `json:"code"`
		OverlayPath string   `json:"overlay_path"`
		ProjectPath string   `json:"project_path"`
		Command     string   `json:"command"`
		Args        []string `json:"args"`
		StartedAt   string   `json:"started_at"`
		Status      string   `json:"status"`
		LastSeen    string   `json:"last_seen"`
	}

	return json.Marshal(sessionJSON{
		Code:        s.Code,
		OverlayPath: s.OverlayPath,
		ProjectPath: s.ProjectPath,
		Command:     s.Command,
		Args:        s.Args,
		StartedAt:   s.StartedAt.Format(time.RFC3339),
		Status:      string(s.Status),
		LastSeen:    s.LastSeen.Format(time.RFC3339),
	})
}
