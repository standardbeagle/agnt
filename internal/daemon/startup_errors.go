package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
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

// daemonStartupLog records a daemon-wide startup event. These entries are
// intentionally global-only: they describe the daemon/socket lifecycle before
// any project session exists.
func (d *Daemon) daemonStartupLog(level, eventType, message string) {
	d.daemonStartupLogPort(level, eventType, message, 0)
}

// daemonStartupLogPort is daemonStartupLog for events that carry an offending
// port (startup port cleanup, port-cleanup timeouts). It is the single
// construction point for daemon-wide (unscoped, empty-ProcessID) startup
// entries, so no caller hand-rolls a &StartupLogEntry{...Timestamp: time.Now()}.
func (d *Daemon) daemonStartupLogPort(level, eventType, message string, port int) {
	if d == nil || d.startupErrorStore == nil {
		return
	}
	// The store is a 100-entry ring shared by every project, and it does not
	// outlive the process. Shutdown emits ~36 daemon-wide info breadcrumbs, which
	// would evict a project's real startup errors on their way out — for a reader
	// that no longer exists, since a stopping daemon serves no queries. Warnings
	// and errors still go in: those explain a shutdown that went wrong.
	if level == "info" && d.isShuttingDown() {
		debug.Log("daemon", "shutdown breadcrumb %s: %s", eventType, message)
		return
	}
	d.startupErrorStore.Add(&StartupLogEntry{
		Level:     level,
		EventType: eventType,
		Message:   message,
		Port:      port,
		Timestamp: time.Now(),
	})
}

// recordStartupEntry is the single construction point for startup-log entries
// keyed by an already-formed ProcessID that the caller holds (a script/proxy
// process ID, event.ScriptID, event.ProxyID, etc.). It removes the hand-rolled
// &StartupLogEntry{...Timestamp: time.Now()} boilerplate that was duplicated
// across the daemon. Prefer startupLog(projectPath) when you only have a
// script/proxy name (it stamps the project prefix for you); use
// daemonStartupLog for genuinely daemon-wide events with no ProcessID.
func (d *Daemon) recordStartupEntry(processID, scriptName, level, eventType, message string, port int) {
	if d == nil || d.startupErrorStore == nil {
		return
	}
	d.startupErrorStore.Add(&StartupLogEntry{
		ProcessID:  processID,
		ScriptName: scriptName,
		Level:      level,
		EventType:  eventType,
		Message:    message,
		Port:       port,
		Timestamp:  time.Now(),
	})
}

// startupLogger binds a StartupLogStore to a single project path so every
// entry it records is automatically stamped with that project's ProcessID
// prefix (makeProcessID(projectPath, scriptName)).
//
// Project scoping of the startup log is a structural property of this type:
// the default startup_log query is project-scoped and filters
// by the "basename-hash:" ProcessID prefix (see StartupLogFilter.ProjectPath).
// The raw store helpers take a free-form ProcessID, which made it easy to
// record a project event with an empty ProcessID — those entries silently
// failed the prefix match and were invisible to every non-global query. The
// logger removes that footgun: callers pass only the script/proxy name (or ""
// for a project-level event) and the project stamp is applied unconditionally.
// makeProcessID(projectPath, "") yields exactly the scoped prefix, so even
// nameless project-level events remain visible to a scoped query.
//
// Genuinely daemon-wide events (shutdown, orphan scans) that must remain
// global-only deliberately bypass this logger and call the store directly with
// no project path.
type startupLogger struct {
	store       *StartupLogStore
	projectPath string
}

// startupLog returns a project-bound logger over the daemon's startup log store.
func (d *Daemon) startupLog(projectPath string) *startupLogger {
	return &startupLogger{store: d.startupErrorStore, projectPath: projectPath}
}

// surfaceConfigWarnings pushes each non-fatal config-parse warning (e.g. a
// depends-on target that isn't autostart, which would otherwise hang the
// dependent script forever with a 0 = wait-indefinitely timeout) into the
// project-scoped startup log so the agent can see it. Parse-time debug.Log
// alone is not sufficient under the Silent Failure Prohibition.
func (d *Daemon) surfaceConfigWarnings(projectPath string, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	log := d.startupLog(projectPath)
	for _, w := range warnings {
		log.Warn("", "config_warning", w)
	}
}

func (l *startupLogger) record(level, name, eventType, message string, port int) {
	if l == nil || l.store == nil {
		return
	}
	l.store.Add(&StartupLogEntry{
		ProcessID:  makeProcessID(l.projectPath, name),
		ScriptName: name,
		Level:      level,
		EventType:  eventType,
		Message:    message,
		Port:       port,
		Timestamp:  time.Now(),
	})
}

// Info, Warn, Error record a project-stamped entry. name is the script/proxy
// name, or "" for a project-level event (still scoped via the project prefix).
func (l *startupLogger) Info(name, eventType, message string) {
	l.record("info", name, eventType, message, 0)
}
func (l *startupLogger) Warn(name, eventType, message string) {
	l.record("warning", name, eventType, message, 0)
}
func (l *startupLogger) Error(name, eventType, message string) {
	l.record("error", name, eventType, message, 0)
}

// WarnPort and ErrorPort record a port-bearing entry (port conflicts,
// listen-port clashes) so the offending port survives into the log.
func (l *startupLogger) WarnPort(name, eventType, message string, port int) {
	l.record("warning", name, eventType, message, port)
}
func (l *startupLogger) ErrorPort(name, eventType, message string, port int) {
	l.record("error", name, eventType, message, port)
}

// StartupLogFilter controls which entries are returned by Query.
type StartupLogFilter struct {
	Since     time.Time
	ProcessID string
	Level     string // "info", "warning", "error", or "" for all
	Limit     int

	// ProjectPath scopes results to a single project. Startup log entries
	// are not stamped with a project path at ingest, but their ProcessID
	// is built by makeProcessID(projectPath, name) which deterministically
	// encodes the project as a "basename-hash:" prefix. When set, only
	// entries whose ProcessID carries that prefix are returned. Entries
	// with a bare (non-project) ProcessID — daemon-wide shutdown/scan
	// events — are therefore visible only to a global (unscoped) query.
	// Empty means no project scoping (the global/legacy behaviour).
	ProjectPath string
}

// Query returns matching entries ordered oldest to newest.
func (s *StartupLogStore) Query(filter StartupLogFilter) []*StartupLogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Derive the deterministic "basename-hash:" ProcessID prefix for the
	// scoped project once, outside the loop. makeProcessID with an empty
	// name yields exactly that prefix.
	var projectPrefix string
	if filter.ProjectPath != "" {
		projectPrefix = makeProcessID(filter.ProjectPath, "")
	}

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
		if projectPrefix != "" && !strings.HasPrefix(entry.ProcessID, projectPrefix) {
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

// ClearByPrefix removes only entries whose ProcessID carries the given
// project prefix (makeProcessID(projectPath, "")), preserving entries for
// other projects and daemon-wide (bare-ProcessID) events. The ring buffer is
// shared across every project, so the last-session cleanup path must scope its
// wipe: an unscoped Clear() tearing down project A's session would otherwise
// destroy project B's startup log. Survivors are re-packed oldest→newest.
func (s *StartupLogStore) ClearByPrefix(prefix string) {
	if prefix == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	survivors := make([]*StartupLogEntry, 0, s.len)
	for i := 0; i < s.len; i++ {
		entry := s.entries[(s.head+i)%s.maxSize]
		if entry == nil {
			continue
		}
		if strings.HasPrefix(entry.ProcessID, prefix) {
			continue
		}
		survivors = append(survivors, entry)
	}

	for i := range s.entries {
		s.entries[i] = nil
	}
	s.head = 0
	s.len = 0
	for _, entry := range survivors {
		s.entries[s.len] = entry
		s.len++
	}
}

// Len returns the current number of entries.
func (s *StartupLogStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.len
}
