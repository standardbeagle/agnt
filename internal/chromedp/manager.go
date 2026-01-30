package chromedp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	// ErrSessionExists is returned when trying to create a session with an existing ID.
	ErrSessionExists = errors.New("session already exists")
	// ErrSessionNotFound is returned when a session ID is not found.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionAmbiguous is returned when a fuzzy lookup matches multiple sessions.
	ErrSessionAmbiguous = errors.New("session ID is ambiguous - multiple matches")
)

// SessionManager manages chromedp automation sessions.
// It uses lock-free sync.Map for the session registry and atomic counters
// for metrics, following the same patterns as browser.Manager.
type SessionManager struct {
	sessions     sync.Map // map[string]*AutomationSession
	active       atomic.Int32
	totalStarted atomic.Int64
	totalFailed  atomic.Int64
	shuttingDown atomic.Bool
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{}
}

// Start starts an automation session with the given configuration.
func (m *SessionManager) Start(ctx context.Context, id string, config SessionConfig) (*AutomationSession, error) {
	if m.shuttingDown.Load() {
		return nil, fmt.Errorf("session manager is shutting down")
	}

	// Check if session already exists
	if _, loaded := m.sessions.Load(id); loaded {
		return nil, ErrSessionExists
	}

	// Ensure ID is set in config
	config.ID = id

	session := NewSession(config)

	// Store before starting to prevent race
	if _, loaded := m.sessions.LoadOrStore(id, session); loaded {
		return nil, ErrSessionExists
	}

	if err := session.Start(ctx); err != nil {
		m.sessions.Delete(id)
		m.totalFailed.Add(1)
		return nil, err
	}

	m.active.Add(1)
	m.totalStarted.Add(1)

	// Clean up when session exits
	go func() {
		<-session.Done()
		m.sessions.Delete(id)
		m.active.Add(-1)
	}()

	return session, nil
}

// Stop stops a session by ID.
func (m *SessionManager) Stop(ctx context.Context, id string) error {
	value, ok := m.sessions.Load(id)
	if !ok {
		return ErrSessionNotFound
	}

	session := value.(*AutomationSession)
	return session.Stop(ctx)
}

// Get returns a session by ID with fuzzy matching support.
// First tries exact match, then looks for sessions where the ID contains
// the search string as a component (for compound IDs).
func (m *SessionManager) Get(id string) (*AutomationSession, error) {
	return m.GetWithPathFilter(id, "")
}

// GetWithPathFilter retrieves a session by ID with fuzzy matching, filtered by path.
// If pathFilter is non-empty, only sessions with matching Path are considered for fuzzy lookup.
// Exact matches are always returned regardless of path filter.
func (m *SessionManager) GetWithPathFilter(id, pathFilter string) (*AutomationSession, error) {
	// First try exact match (lock-free read) - always works regardless of path
	if val, ok := m.sessions.Load(id); ok {
		return val.(*AutomationSession), nil
	}

	// Normalize path filter for comparison
	normalizedFilter := normalizePath(pathFilter)

	// Fuzzy match: look for session where the ID contains the search string as a component
	// Compound ID format: {project-hash}:{session-name}
	var matches []*AutomationSession
	m.sessions.Range(func(key, value any) bool {
		sessionID := key.(string)
		session := value.(*AutomationSession)

		// If path filter is specified, only consider sessions in that path
		if normalizedFilter != "" && normalizedFilter != "." {
			sessionPath := normalizePath(session.Path())
			if sessionPath != normalizedFilter {
				return true // Skip this session, continue iteration
			}
		}

		// Check if search string matches a component of the compound ID
		// Split by ":" and check each part
		parts := strings.Split(sessionID, ":")
		for _, part := range parts {
			if part == id {
				matches = append(matches, session)
				break
			}
		}
		return true
	})

	if len(matches) == 0 {
		return nil, ErrSessionNotFound
	}
	if len(matches) > 1 {
		return nil, ErrSessionAmbiguous
	}
	return matches[0], nil
}

// normalizePath normalizes a path for comparison.
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	// Remove trailing slashes and normalize
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}

// List returns information about all sessions.
func (m *SessionManager) List() []SessionInfo {
	var infos []SessionInfo
	m.sessions.Range(func(key, value interface{}) bool {
		session := value.(*AutomationSession)
		infos = append(infos, session.Info())
		return true
	})
	return infos
}

// ListByPath returns sessions filtered by project path.
// If pathFilter is empty, returns all sessions.
func (m *SessionManager) ListByPath(pathFilter string) []SessionInfo {
	if pathFilter == "" {
		return m.List()
	}

	normalizedFilter := normalizePath(pathFilter)
	var infos []SessionInfo
	m.sessions.Range(func(key, value interface{}) bool {
		session := value.(*AutomationSession)
		if normalizePath(session.Path()) == normalizedFilter {
			infos = append(infos, session.Info())
		}
		return true
	})
	return infos
}

// StopByProjectPath stops all sessions for a specific project path.
// This is used for session-scoped cleanup when a client disconnects.
// Returns the list of stopped session IDs.
func (m *SessionManager) StopByProjectPath(ctx context.Context, projectPath string) ([]string, error) {
	normalizedPath := normalizePath(projectPath)
	var toStop []*AutomationSession
	m.sessions.Range(func(key, value any) bool {
		session := value.(*AutomationSession)
		if normalizePath(session.Path()) == normalizedPath {
			toStop = append(toStop, session)
		}
		return true
	})

	if len(toStop) == 0 {
		return nil, nil
	}

	var stopWg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error
	var stoppedIDs []string
	var stoppedMu sync.Mutex

	for _, session := range toStop {
		stopWg.Add(1)
		go func(s *AutomationSession) {
			defer stopWg.Done()
			id := s.ID()
			if err := m.Stop(ctx, id); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			} else {
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, id)
				stoppedMu.Unlock()
			}
		}(session)
	}

	done := make(chan struct{})
	go func() {
		stopWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		if len(errs) > 0 {
			errs = append(errs, ctx.Err())
		} else {
			return stoppedIDs, ctx.Err()
		}
	}

	if len(errs) > 0 {
		return stoppedIDs, errors.Join(errs...)
	}
	return stoppedIDs, nil
}

// ActiveCount returns the number of active sessions.
func (m *SessionManager) ActiveCount() int {
	return int(m.active.Load())
}

// TotalStarted returns the total number of sessions started.
func (m *SessionManager) TotalStarted() int64 {
	return m.totalStarted.Load()
}

// TotalFailed returns the total number of sessions that failed to start.
func (m *SessionManager) TotalFailed() int64 {
	return m.totalFailed.Load()
}

// IsShuttingDown returns true if the manager is shutting down.
func (m *SessionManager) IsShuttingDown() bool {
	return m.shuttingDown.Load()
}

// StopAll stops all running sessions.
// Unlike Shutdown, this does NOT set shuttingDown flag, allowing new sessions
// to be started afterward. This is used for cleanup when the last client disconnects.
func (m *SessionManager) StopAll(ctx context.Context) ([]string, error) {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var stoppedIDs []string
	var stoppedMu sync.Mutex

	m.sessions.Range(func(key, value interface{}) bool {
		session := value.(*AutomationSession)
		wg.Add(1)
		go func(s *AutomationSession) {
			defer wg.Done()
			id := s.ID()
			if err := s.Stop(ctx); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			} else {
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, id)
				stoppedMu.Unlock()
			}
		}(session)
		return true
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return stoppedIDs, firstErr
	case <-ctx.Done():
		return stoppedIDs, ctx.Err()
	}
}

// Shutdown stops all sessions and prevents new ones from starting.
func (m *SessionManager) Shutdown(ctx context.Context) error {
	m.shuttingDown.Store(true)

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	m.sessions.Range(func(key, value interface{}) bool {
		session := value.(*AutomationSession)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := session.Stop(ctx); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
		return true
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return firstErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns manager statistics.
type ManagerStats struct {
	ActiveSessions int   `json:"active_sessions"`
	TotalStarted   int64 `json:"total_started"`
	TotalFailed    int64 `json:"total_failed"`
	ShuttingDown   bool  `json:"shutting_down"`
}

// Stats returns current manager statistics.
func (m *SessionManager) Stats() ManagerStats {
	return ManagerStats{
		ActiveSessions: m.ActiveCount(),
		TotalStarted:   m.TotalStarted(),
		TotalFailed:    m.TotalFailed(),
		ShuttingDown:   m.IsShuttingDown(),
	}
}
