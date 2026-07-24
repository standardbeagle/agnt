// Package daemon provides the background daemon for persistent state management.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// ErrSessionExists is returned by SessionRegistry.Register when a session with
// the same code is already registered. Callers distinguish this (a reconnect)
// from other Register failures (e.g. empty code) via errors.Is so a malformed
// registration is not silently treated as a reconnect.
var ErrSessionExists = errors.New("session already exists")

// SessionStatus represents the current state of a session.
type SessionStatus string

const (
	// SessionStatusActive indicates the session is running and responsive.
	SessionStatusActive SessionStatus = "active"
	// SessionStatusDisconnected indicates the session has not sent a heartbeat recently.
	SessionStatusDisconnected SessionStatus = "disconnected"
)

// SessionKind distinguishes who owns the PTY child behind a Session entry.
// See docs/superpowers/specs/2026-07-03-remote-ssh-design.md §2.3: both
// flavors share this same struct and SessionRegistry so project-scoping
// consumers (hasOtherSessions, FindByDirectory, List/ListActive) keep working
// unmodified — only cleanup dispatch and attach handling branch on Kind.
type SessionKind string

const (
	// SessionKindClassic is the existing agnt-run-owns-the-PTY flavor.
	// doCleanupExact's killSessionPGID/killSessionJobObject calls apply to it
	// on client disconnect, same as always.
	SessionKindClassic SessionKind = "classic"
	// SessionKindSessionHost is a daemon-owned detachable PTY session
	// (SESSION-HOST CREATE). Per spec §2.2 invariant 11, it must NOT be
	// torn down by doCleanupExact on attach-stream disconnect — only an
	// explicit SESSION-HOST KILL (or the PTY child exiting on its own, or
	// daemon shutdown's orphan-pgid sweep) ends it. See
	// .claude/rules/daemon-architecture.md § Session Containment.
	SessionKindSessionHost SessionKind = "session-host"
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

	// SessionPGID is the POSIX process group ID of the PTY child. The PTY
	// child is started as its own session leader (via creack/pty's Setsid),
	// so on Unix this equals the PTY child PID and identifies every process
	// the coding agent spawned via non-interactive bash, including
	// backgrounded `npm run dev &` style jobs. Zero on Windows (Job Object
	// cleanup is handled separately) or when the client didn't report one.
	SessionPGID int `json:"session_pgid,omitempty"`

	// OwnerPID identifies the agnt wrapper process that owns a classic session.
	// A dropped control connection is not proof that this process exited: the
	// daemon must confirm OwnerPID is gone before reaping SessionPGID/the Job
	// Object. Zero means ownership is unknown and therefore fails safe.
	OwnerPID int `json:"owner_pid,omitempty"`

	// SessionJobHandle is the Windows Job Object handle for the PTY child
	// subtree, reported by `agnt run` on Windows. The Windows equivalent
	// of SessionPGID: every process assigned to this job (and its
	// descendants) is killed when the job is terminated via
	// platform.KillSessionJobObject. Zero on Unix or when the client
	// didn't report one. Stored as uint64 because windows.Handle is not
	// available outside the Windows build-tagged platform package.
	//
	// Caveat: job handles are per-process on Windows. This field is a
	// best-effort hint for the daemon; the authoritative containment is
	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE on the owning `agnt run`
	// process. When the daemon IS the owner (shared process) or when
	// the handle is still valid cross-process (e.g. via inheritance),
	// explicit KillSessionJobObject at session cleanup accelerates
	// teardown.
	SessionJobHandle uint64 `json:"session_job_handle,omitempty"`

	// Kind distinguishes classic (agnt-run-owned PTY) from session-host
	// (daemon-owned PTY) sessions. Zero value normalizes to
	// SessionKindClassic in ToJSON/MarshalJSON so existing callers that
	// never set it see unchanged wire output.
	Kind SessionKind `json:"kind,omitempty"`

	// Internal fields (not serialized)
	mu sync.RWMutex
}

// kindOrClassic normalizes an empty Kind to SessionKindClassic for wire
// output, so pre-existing REGISTER callers that never set Kind are
// unaffected.
func (s *Session) kindOrClassic() SessionKind {
	if s.Kind == "" {
		return SessionKindClassic
	}
	return s.Kind
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

// GetLastSeen returns the last-seen timestamp under the read lock. Direct reads
// of s.LastSeen race with UpdateLastSeen / markDisconnectedIfStale, which write
// it under s.mu — callers outside the session must use this accessor.
func (s *Session) GetLastSeen() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastSeen
}

// markDisconnectedIfStale transitions an active session to disconnected when
// its last heartbeat predates cutoff, and reports whether it made that
// transition. The check and the write happen under one lock so the session
// owns its own state-machine invariant — the registry no longer reaches in to
// mutate Status/LastSeen directly.
func (s *Session) markDisconnectedIfStale(cutoff time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == SessionStatusActive && s.LastSeen.Before(cutoff) {
		s.Status = SessionStatusDisconnected
		return true
	}
	return false
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
		"code":               s.Code,
		"overlay_path":       s.OverlayPath,
		"project_path":       s.ProjectPath,
		"command":            s.Command,
		"args":               s.Args,
		"started_at":         s.StartedAt.Format(time.RFC3339),
		"status":             string(s.Status),
		"last_seen":          s.LastSeen.Format(time.RFC3339),
		"session_pgid":       s.SessionPGID,
		"owner_pid":          s.OwnerPID,
		"session_job_handle": s.SessionJobHandle,
		"kind":               string(s.kindOrClassic()),
	}
}

// SessionRegistry manages active sessions with lock-free operations.
type SessionRegistry struct {
	sessions sync.Map // map[string]*Session

	// Statistics (atomics for lock-free access). These are monotonic
	// lifetime counters. The number of *currently active* sessions is NOT
	// stored here — it is derived on demand by ActiveCount() from each
	// session's authoritative Status. A standalone active counter drifts:
	// CheckHeartbeats decrements on Active→Disconnected, but Heartbeat
	// revives Disconnected→Active and UnregisterExact deletes regardless of
	// status, so the increments and decrements never stayed balanced.
	totalRegistered   atomic.Int64
	totalUnregistered atomic.Int64

	// Heartbeat timeout configuration
	heartbeatTimeout time.Duration
}

// HeartbeatTimeout returns the configured stale-session cutoff duration. The
// doctor's session-health check reads it instead of hardcoding a parallel
// constant, so the two never drift.
func (r *SessionRegistry) HeartbeatTimeout() time.Duration {
	return r.heartbeatTimeout
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
		return fmt.Errorf("%w: %q", ErrSessionExists, session.Code)
	}

	r.totalRegistered.Add(1)
	return nil
}

// ReplaceExact replaces the current registration only when it is expected.
// Reconnects use a fresh *Session identity so cleanup captured for an older
// registration cannot retire the new lifetime.
func (r *SessionRegistry) ReplaceExact(code string, expected, replacement *Session) bool {
	if expected == nil || replacement == nil || replacement.Code != code {
		return false
	}
	return r.sessions.CompareAndSwap(code, expected, replacement)
}

// UnregisterExact removes the current registration only when it is expected.
func (r *SessionRegistry) UnregisterExact(code string, expected *Session) bool {
	if expected == nil || !r.sessions.CompareAndDelete(code, expected) {
		return false
	}
	r.totalUnregistered.Add(1)
	return true
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
	r.sessions.Range(func(_, value interface{}) bool {
		value.(*Session).markDisconnectedIfStale(cutoff)
		return true
	})
}

// ActiveCount returns the number of currently active sessions, derived from
// each session's authoritative Status. Deriving (rather than maintaining a
// counter) makes the count impossible to drift across the
// Active↔Disconnected revive cycle and the delete-on-any-status
// UnregisterExact.
func (r *SessionRegistry) ActiveCount() int64 {
	var n int64
	r.sessions.Range(func(_, value interface{}) bool {
		if value.(*Session).IsActive() {
			n++
		}
		return true
	})
	return n
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

// Info returns statistics about the session registry.
func (r *SessionRegistry) Info() protocol.SessionInfo {
	return protocol.SessionInfo{
		ActiveCount:       r.ActiveCount(),
		TotalRegistered:   r.totalRegistered.Load(),
		TotalUnregistered: r.totalUnregistered.Load(),
	}
}

// sessionMoreRecent reports whether session a should be preferred over b on a
// depth tie: the most recently started session wins, with the unique Code as a
// final deterministic tiebreak when StartedAt is identical. StartedAt and Code
// are set once at registration and effectively immutable, so reading them
// without the session lock is safe (matching FindByDirectory's unlocked read of
// ProjectPath).
func sessionMoreRecent(a, b *Session) bool {
	return a.StartedAt.After(b.StartedAt) || (a.StartedAt.Equal(b.StartedAt) && a.Code > b.Code)
}

// FindByDirectory finds an active session whose project path matches the given directory
// or any of its parent directories. Returns the most specific (deepest) match.
// This enables auto-attach behavior where MCP clients in subdirectories can find
// sessions started in parent directories.
//
// On equal-depth ties (same project path), the most recently started session
// wins (Code breaks exact ties), making selection deterministic rather than
// dependent on undefined sync.Map.Range order.
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
			if depth > bestMatchDepth || (depth == bestMatchDepth && bestMatch != nil && sessionMoreRecent(session, bestMatch)) {
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
		Code             string   `json:"code"`
		OverlayPath      string   `json:"overlay_path"`
		ProjectPath      string   `json:"project_path"`
		Command          string   `json:"command"`
		Args             []string `json:"args"`
		StartedAt        string   `json:"started_at"`
		Status           string   `json:"status"`
		LastSeen         string   `json:"last_seen"`
		SessionPGID      int      `json:"session_pgid,omitempty"`
		OwnerPID         int      `json:"owner_pid,omitempty"`
		SessionJobHandle uint64   `json:"session_job_handle,omitempty"`
		Kind             string   `json:"kind,omitempty"`
	}

	return json.Marshal(sessionJSON{
		Code:             s.Code,
		OverlayPath:      s.OverlayPath,
		ProjectPath:      s.ProjectPath,
		Command:          s.Command,
		Args:             s.Args,
		StartedAt:        s.StartedAt.Format(time.RFC3339),
		Status:           string(s.Status),
		LastSeen:         s.LastSeen.Format(time.RFC3339),
		SessionPGID:      s.SessionPGID,
		OwnerPID:         s.OwnerPID,
		SessionJobHandle: s.SessionJobHandle,
		Kind:             string(s.kindOrClassic()),
	})
}
