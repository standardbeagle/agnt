package chromedp

import (
	"context"
	"errors"
	"fmt"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"slices"
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
	// ErrSessionCapReached is returned by Start when the concurrent-session
	// ceiling is reached. Wrapped with an actionable message naming the limit
	// and how to free a slot (Silent Failure Prohibition,
	// .claude/rules/daemon-architecture.md).
	ErrSessionCapReached = errors.New("concurrent browser automation session cap reached")
)

// DefaultMaxSessions is the default ceiling on concurrent chromedp automation
// sessions. It is deliberately single-digit, not 100 like PageTracker/daemon
// client caps: every session is a full headless Chrome process (hundreds of MB
// RSS plus its own renderer subprocesses), so four concurrent sessions already
// costs on the order of a gigabyte. The failure mode of an uncapped manager is
// an agent looping the automation/browser tools until the host swaps and OOMs —
// far more expensive than a leaked lightweight process. Four leaves room for
// modest parallel automation (e.g. a handful of concurrent audits) while
// keeping the worst case well below a developer machine's memory budget.
// Raise it deliberately via automation.max-sessions in .agnt.kdl.
//
// Kept in sync with config.DefaultAutomationMaxSessions (config must not import
// chromedp; both are 4).
const DefaultMaxSessions = 4

// SessionManager manages chromedp automation sessions.
// It uses lock-free sync.Map for the session registry and atomic counters
// for metrics, following the same patterns as browser.Manager.
type SessionManager struct {
	sessions     sync.Map // map[string]*AutomationSession
	active       atomic.Int32
	maxSessions  atomic.Int32 // daemon-wide ceiling; <=0 means DefaultMaxSessions
	baseMax      atomic.Int32 // ceiling used while no project declares a quota
	totalStarted atomic.Int64
	totalFailed  atomic.Int64
	shuttingDown atomic.Bool

	// projectQuotas holds each connected project's own ceiling, keyed by
	// normalized project path (map[string]*projectQuota). Read by Start on every
	// admission; written by SetProjectMaxSessions / ClearProjectMaxSessions on
	// session connect and project teardown. Not user-visible on its own — the
	// refusal message in Start is where a quota surfaces.
	projectQuotas sync.Map

	// startSession launches the browser for an already-reserved session.
	// Overridable in tests so the concurrency cap can be exercised without
	// spawning real Chrome processes. Defaults to (*AutomationSession).Start.
	startSession func(ctx context.Context, s *AutomationSession) error
}

// NewSessionManager creates a new session manager with the default ceiling
// (DefaultMaxSessions) on concurrent sessions.
func NewSessionManager() *SessionManager {
	return NewSessionManagerWithMax(DefaultMaxSessions)
}

// NewSessionManagerWithMax creates a session manager with an explicit
// concurrent-session ceiling. A max <= 0 falls back to DefaultMaxSessions.
func NewSessionManagerWithMax(max int) *SessionManager {
	m := &SessionManager{
		startSession: func(ctx context.Context, s *AutomationSession) error {
			return s.Start(ctx)
		},
	}
	m.SetMaxSessions(max)
	return m
}

// projectQuota is one project's ceiling and its live session count.
type projectQuota struct {
	max    atomic.Int32
	active atomic.Int32
}

// SetMaxSessions updates the daemon-wide ceiling used while no project has
// declared its own quota. A max <= 0 restores DefaultMaxSessions. Safe to call
// concurrently with Start. Lowering the ceiling below the current active count
// never evicts a session an agent is mid-way through using; it only refuses new
// Start calls until the count drops.
func (m *SessionManager) SetMaxSessions(max int) {
	if max <= 0 {
		max = DefaultMaxSessions
	}
	m.baseMax.Store(int32(max))
	m.recomputeCeiling()
}

// SetProjectMaxSessions records one project's automation.max-sessions. The
// project's own sessions are counted against this value, so a second project
// connecting with a different value cannot change the first project's quota.
// The daemon-wide ceiling becomes the largest quota any connected project
// declares: each Chrome is a host-memory cost, so the host total stays bounded
// by the most any single operator config authorised rather than by the sum.
// A max <= 0 means DefaultMaxSessions. An empty path is ignored — sessions with
// no project are bounded by the daemon-wide ceiling alone.
func (m *SessionManager) SetProjectMaxSessions(projectPath string, max int) {
	key := projectQuotaKey(projectPath)
	if key == "" {
		return
	}
	if max <= 0 {
		max = DefaultMaxSessions
	}
	q, _ := m.projectQuotas.LoadOrStore(key, &projectQuota{})
	q.(*projectQuota).max.Store(int32(max))
	m.recomputeCeiling()
}

// ClearProjectMaxSessions drops a project's declared quota when the project's
// last session leaves, so a departed project stops propping up the daemon-wide
// ceiling. The entry is kept while the project still has live sessions (their
// exit goroutines release against it); it then reverts to the default quota.
func (m *SessionManager) ClearProjectMaxSessions(projectPath string) {
	key := projectQuotaKey(projectPath)
	if key == "" {
		return
	}
	if v, ok := m.projectQuotas.Load(key); ok {
		q := v.(*projectQuota)
		if q.active.Load() > 0 {
			q.max.Store(0)
		} else {
			m.projectQuotas.CompareAndDelete(key, v)
		}
	}
	m.recomputeCeiling()
}

// ProjectMaxSessions returns the quota a project's sessions are held to: its
// declared value, or the daemon-wide ceiling when it has declared none.
func (m *SessionManager) ProjectMaxSessions(projectPath string) int {
	if v, ok := m.projectQuotas.Load(projectQuotaKey(projectPath)); ok {
		if max := v.(*projectQuota).max.Load(); max > 0 {
			return int(max)
		}
	}
	return m.MaxSessions()
}

// recomputeCeiling sets the daemon-wide ceiling to the largest declared project
// quota, or to the base ceiling when no project declares one.
func (m *SessionManager) recomputeCeiling() {
	var largest int32
	m.projectQuotas.Range(func(_, v any) bool {
		if max := v.(*projectQuota).max.Load(); max > largest {
			largest = max
		}
		return true
	})
	if largest == 0 {
		largest = m.baseMax.Load()
	}
	if largest <= 0 {
		largest = DefaultMaxSessions
	}
	m.maxSessions.Store(largest)
}

func projectQuotaKey(projectPath string) string {
	if projectPath == "" {
		return ""
	}
	return pathutil.NormalizeTrailingSlash(projectPath)
}

// MaxSessions returns the current concurrent-session ceiling.
func (m *SessionManager) MaxSessions() int {
	return int(m.maxSessions.Load())
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

	// Reserve a concurrency slot BEFORE the expensive Chrome launch. The CAS
	// loop is the single atomic admission point: the check and the increment
	// are one indivisible step, so no number of racing Start calls can drive
	// active past the ceiling (the load-then-add TOCTOU window of the
	// ProcessManager Start/Shutdown bug — publish-security-review-lessons.md
	// §4 — is structurally avoided). A CompareAndSwap that only commits when
	// cur < max also means ActiveCount() never even momentarily exceeds the
	// cap, unlike an add-then-rollback. Refuse LOUDLY at the cap rather than
	// silently queueing or evicting a session an agent is mid-way through
	// using (Silent Failure Prohibition).
	//
	// The project's own quota is reserved first, then the daemon-wide ceiling;
	// a refusal at the second releases the first.
	var quota *projectQuota
	if v, ok := m.projectQuotas.Load(projectQuotaKey(config.Path)); ok {
		quota = v.(*projectQuota)
		projectMax := quota.max.Load()
		if projectMax <= 0 {
			projectMax = DefaultMaxSessions
		}
		for {
			cur := quota.active.Load()
			if cur >= projectMax {
				m.sessions.CompareAndDelete(id, session)
				return nil, fmt.Errorf(
					"cannot start browser automation session %q: %w of %d for this project "+
						"(each session is a full Chrome process using hundreds of MB). "+
						"Stop an existing session (browser/automation stop) or raise "+
						"automation.max-sessions in .agnt.kdl",
					id, ErrSessionCapReached, projectMax)
			}
			if quota.active.CompareAndSwap(cur, cur+1) {
				break
			}
		}
	}
	releaseProject := func() {
		if quota != nil {
			quota.active.Add(-1)
		}
	}

	max := m.maxSessions.Load()
	for {
		cur := m.active.Load()
		if cur >= max {
			m.sessions.CompareAndDelete(id, session)
			releaseProject()
			return nil, fmt.Errorf(
				"cannot start browser automation session %q: %w of %d across all projects "+
					"(each session is a full Chrome process using hundreds of MB). "+
					"Stop an existing session (browser/automation stop) or raise "+
					"automation.max-sessions in .agnt.kdl",
				id, ErrSessionCapReached, max)
		}
		if m.active.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	if err := m.startSession(ctx, session); err != nil {
		m.sessions.CompareAndDelete(id, session)
		m.active.Add(-1) // release the reserved slot
		releaseProject()
		m.totalFailed.Add(1)
		return nil, err
	}

	m.totalStarted.Add(1)

	// Clean up when session exits
	go func() {
		<-session.Done()
		m.sessions.CompareAndDelete(id, session)
		m.active.Add(-1)
		releaseProject()
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
	normalizedFilter := pathutil.NormalizeTrailingSlash(pathFilter)

	// Fuzzy match: look for session where the ID contains the search string as a component
	// Compound ID format: {project-hash}:{session-name}
	var matches []*AutomationSession
	m.sessions.Range(func(key, value any) bool {
		sessionID := key.(string)
		session := value.(*AutomationSession)

		// If path filter is specified, only consider sessions in that path
		if normalizedFilter != "" && normalizedFilter != "." {
			sessionPath := pathutil.NormalizeTrailingSlash(session.Path())
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

	normalizedFilter := pathutil.NormalizeTrailingSlash(pathFilter)
	var infos []SessionInfo
	m.sessions.Range(func(key, value interface{}) bool {
		session := value.(*AutomationSession)
		if pathutil.NormalizeTrailingSlash(session.Path()) == normalizedFilter {
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
	normalizedPath := pathutil.NormalizeTrailingSlash(projectPath)
	var toStop []*AutomationSession
	m.sessions.Range(func(key, value any) bool {
		session := value.(*AutomationSession)
		if pathutil.NormalizeTrailingSlash(session.Path()) == normalizedPath {
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
		// The stop goroutines may still be appending: read both lists under
		// their locks, and copy them so a late append cannot write into the
		// slice the caller now holds.
		errMu.Lock()
		errs = append(slices.Clone(errs), ctx.Err())
		errMu.Unlock()
		stoppedMu.Lock()
		ids := slices.Clone(stoppedIDs)
		stoppedMu.Unlock()
		if len(errs) == 1 {
			return ids, ctx.Err()
		}
		return ids, errors.Join(errs...)
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
		// Stops may still be in flight; copy the list under its lock.
		stoppedMu.Lock()
		defer stoppedMu.Unlock()
		return slices.Clone(stoppedIDs), ctx.Err()
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
