// Package scope defines the session/project scoping token that gates
// cross-session delivery in the daemon. The design goal is to make global
// (cross-project) access the loud, deliberate exception rather than the
// silent default.
//
// Almost every delivery path — proxy lookups, overlay endpoint resolution,
// broadcast fan-out — takes a Scope. A Scope built with Project(path) only
// matches that one project. A Scope built with Unscoped(reason) matches
// everything but emits an audit log line at the construction site, so every
// global access is greppable and traceable.
//
// The zero value is invalid on purpose: callers must construct a Scope
// explicitly, which prevents "forgot to scope" bugs from compiling into a
// silent global.
package scope

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/standardbeagle/agnt/internal/debug"
)

// Scope identifies which project(s) an operation is allowed to touch.
//
// Construct via Project (single project) or Unscoped (all projects, audited).
// The zero value reports Valid() == false and matches nothing.
type Scope struct {
	projectPath string // normalized; "" when unscoped or invalid
	unscoped    bool
	valid       bool
	reason      string // why this scope is unscoped (audit)
}

// Project returns a Scope restricted to a single project directory. The path
// is normalized so callers may pass a raw, un-normalized directory.
func Project(path string) Scope {
	return Scope{projectPath: NormalizePath(path), valid: true}
}

// Unscoped returns a Scope that matches every project. It is the deliberate
// escape hatch for the handful of daemon-wide operations (shutdown reaps,
// state restore, orphan scans). Every call logs an audit line identifying the
// caller and reason so global access stays rare and visible.
func Unscoped(reason string) Scope {
	if _, file, line, ok := runtime.Caller(1); ok {
		debug.Log("scope", "UNSCOPED scope created reason=%q caller=%s:%d", reason, file, line)
	} else {
		debug.Log("scope", "UNSCOPED scope created reason=%q caller=unknown", reason)
	}
	return Scope{unscoped: true, valid: true, reason: reason}
}

// IsUnscoped reports whether this scope matches every project.
func (s Scope) IsUnscoped() bool { return s.unscoped }

// Valid reports whether this scope was constructed (not a zero value).
func (s Scope) Valid() bool { return s.valid }

// ProjectPath returns the normalized project path, or "" when unscoped.
func (s Scope) ProjectPath() string { return s.projectPath }

// Reason returns the audit reason supplied to Unscoped, or "".
func (s Scope) Reason() string { return s.reason }

// Match reports whether the given project directory falls within this scope.
// An unscoped scope matches anything. An invalid (zero) scope matches nothing.
// The argument is normalized before comparison.
func (s Scope) Match(path string) bool {
	if !s.valid {
		return false
	}
	if s.unscoped {
		return true
	}
	if s.projectPath == "" {
		return false
	}
	return NormalizePath(path) == s.projectPath
}

// NormalizePath is the single canonical path normalizer used for all scope
// comparisons. It resolves to an absolute path and lowercases on Windows for
// case-insensitive filesystems. Empty and "." normalize to "".
func NormalizePath(path string) string {
	if path == "" || path == "." {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}
