package license

import (
	"fmt"
	"sync"
	"time"
)

// Capability is a Pro capability key. A license payload lists the capabilities
// it grants; Check authorizes a feature only if its capability is granted and
// the license is in a serving state.
type Capability string

const (
	// CapWholeSite gates whole-site / multi-page crawling and operations
	// (vs. the free single-page tools).
	CapWholeSite Capability = "whole_site"
	// CapAnalysisReport gates generated analysis / reporting features.
	CapAnalysisReport Capability = "analysis_report"
	// CapComponentExtract gates component-library / CSS extraction from a
	// codebase.
	CapComponentExtract Capability = "component_extract"
	// CapAdvancedTesting gates the advanced testing suite.
	CapAdvancedTesting Capability = "advanced_testing"
)

// now is overridable in tests; production uses time.Now.
var now = time.Now

// Manager loads and caches the installed license, and is the single chokepoint
// Pro features consult via Check. Validation (ECDSA verify) happens once at
// load, never on the hot path.
type Manager struct {
	mu      sync.RWMutex
	loaded  bool
	status  Status
	loadErr error // unexpected I/O error from the last Load; nil on success
}

// NewManager returns an unloaded Manager. Call Load (or let Check lazy-load)
// before relying on its state.
func NewManager() *Manager { return &Manager{} }

// NewManagerForTest returns a Manager preloaded with the given Status, bypassing
// disk load and signature validation. It exists so external-package tests can
// build a Manager that grants (or denies) a capability deterministically. For
// tests only — production code must use NewManager + Load.
func NewManagerForTest(s Status) *Manager {
	m := &Manager{}
	m.set(s, nil)
	return m
}

// Load reads, verifies, and evaluates the stored license, caching the result.
// A missing license is not an error — it yields StateMissing. A present but
// unverifiable blob yields StateInvalid (also not an error: the agent should
// see the state, not a failure). Returns an error only for unexpected I/O.
func (m *Manager) Load() error {
	blob, err := Load()
	if err == ErrNoLicense {
		m.set(Status{State: StateMissing}, nil)
		return nil
	}
	if err != nil {
		// Cache the failure so Status/Check can report the real problem
		// (e.g. a permission error) instead of misreading the zero-value
		// status as "no license installed".
		m.set(Status{State: StateMissing}, err)
		return err
	}
	payload, err := Validate(blob)
	if err != nil {
		m.set(Status{State: StateInvalid}, nil)
		return nil
	}
	m.set(Evaluate(payload, now()), nil)
	return nil
}

func (m *Manager) set(s Status, loadErr error) {
	m.mu.Lock()
	m.status = s
	m.loadErr = loadErr
	m.loaded = true
	m.mu.Unlock()
}

// Reload forces a re-read from disk (e.g. after `agnt activate`).
func (m *Manager) Reload() error {
	m.mu.Lock()
	m.loaded = false
	m.mu.Unlock()
	return m.Load()
}

// Status returns the cached license status, lazy-loading on first use.
func (m *Manager) Status() Status {
	m.mu.RLock()
	if m.loaded {
		s := m.status
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()
	_ = m.Load()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// LoadError returns the unexpected I/O error from the last Load, if any.
// A non-nil LoadError means the cached status is a placeholder — callers
// must surface the error rather than the (misleading) missing-license state.
func (m *Manager) LoadError() error {
	m.mu.RLock()
	if m.loaded {
		err := m.loadErr
		m.mu.RUnlock()
		return err
	}
	m.mu.RUnlock()
	_ = m.Load()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadErr
}

// GateError is returned by Check when a Pro capability is denied. It carries
// the deciding state so callers can tailor messaging.
type GateError struct {
	Capability Capability
	State      State
}

func (e *GateError) Error() string {
	switch e.State {
	case StateMissing:
		return fmt.Sprintf("%q is a Pro feature — no license installed. Run `agnt activate <key>` to enable it.", e.Capability)
	case StateInvalid:
		return fmt.Sprintf("%q is a Pro feature — the installed license is invalid. Re-run `agnt activate <key>` with a valid key.", e.Capability)
	case StateExpired:
		return fmt.Sprintf("%q is a Pro feature — your license expired more than %d days ago. Renew and run `agnt activate <key>`.", e.Capability, int(GracePeriod.Hours()/24))
	default:
		// Serving state but capability not granted by this license.
		return fmt.Sprintf("%q is not included in your current license plan.", e.Capability)
	}
}

// Check authorizes a Pro capability. It returns nil when the license is in a
// serving state (Valid or Grace) and grants cap; otherwise a *GateError. When
// the state is Grace, it also returns a non-empty warning the caller should
// surface (the error is still nil — Pro is allowed during grace). An
// unexpected license-read I/O error is returned as-is: remediating with
// `agnt activate` cannot fix a disk problem, so it must not be misreported
// as a missing license.
func (m *Manager) Check(cap Capability) (warning string, err error) {
	if loadErr := m.LoadError(); loadErr != nil {
		return "", fmt.Errorf("reading license: %w", loadErr)
	}
	s := m.Status()
	if !s.State.serving() {
		return "", &GateError{Capability: cap, State: s.State}
	}
	if s.Payload == nil || !s.Payload.Grants(cap) {
		return "", &GateError{Capability: cap, State: s.State}
	}
	if s.State == StateGrace {
		warning = fmt.Sprintf("license expired; Pro features keep working for %d more day(s). Renew to avoid interruption.", s.DaysLeft)
	}
	return warning, nil
}
