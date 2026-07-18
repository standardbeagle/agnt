package alert

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MaxPinnedPerProject bounds how many errors a project can have pinned at
// once. Pinning beyond the cap fails loud (the agent must unpin first) rather
// than silently evicting an older pin — a pin is an explicit "keep this".
const MaxPinnedPerProject = 50

// PinnedError is a saved copy of an error the agent chose to keep. It lives
// outside the ring buffers, so every automatic clear (build success, process
// restart, session end, ring overwrite) leaves it untouched. It exists until
// explicitly unpinned; the daemon's lifetime is its retention horizon.
type PinnedError struct {
	ID          string    `json:"id"`       // unified-error id (alert store) or incident fingerprint
	Source      string    `json:"source"`   // "process:<id>", "browser_js", ...
	Severity    string    `json:"severity"` // "error" or "warning"
	Category    string    `json:"category"`
	Message     string    `json:"message"`
	Page        string    `json:"page,omitempty"`
	Tag         string    `json:"tag,omitempty"` // agent-supplied note, e.g. "flaky-db-timeout"
	ProjectPath string    `json:"project_path,omitempty"`
	FirstSeen   time.Time `json:"first_seen"`
	PinnedAt    time.Time `json:"pinned_at"`
}

// PinnedStore holds pinned errors keyed by id within a project. Re-pinning an
// existing id updates its tag (idempotent save).
type PinnedStore struct {
	mu   sync.RWMutex
	pins map[string]map[string]*PinnedError // projectPath → id → pin
}

// NewPinnedStore creates an empty PinnedStore.
func NewPinnedStore() *PinnedStore {
	return &PinnedStore{pins: make(map[string]map[string]*PinnedError)}
}

// Pin saves p, keyed by (ProjectPath, ID). Returns an error when the
// project's pin cap is reached and the id is not already pinned.
func (s *PinnedStore) Pin(p PinnedError) error {
	if p.ID == "" {
		return fmt.Errorf("pin requires an id")
	}
	if p.PinnedAt.IsZero() {
		p.PinnedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	proj := s.pins[p.ProjectPath]
	if proj == nil {
		proj = make(map[string]*PinnedError)
		s.pins[p.ProjectPath] = proj
	}
	if _, exists := proj[p.ID]; !exists && len(proj) >= MaxPinnedPerProject {
		return fmt.Errorf("pin cap reached (%d) for project %s: unpin something first", MaxPinnedPerProject, p.ProjectPath)
	}
	proj[p.ID] = &p
	return nil
}

// Unpin removes the pin with the given id from the project. Returns whether a
// pin was actually removed.
func (s *PinnedStore) Unpin(projectPath, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	proj := s.pins[projectPath]
	if proj == nil {
		return false
	}
	if _, ok := proj[id]; !ok {
		return false
	}
	delete(proj, id)
	if len(proj) == 0 {
		delete(s.pins, projectPath)
	}
	return true
}

// List returns the project's pins (or every project's when global), newest
// pin first.
func (s *PinnedStore) List(projectPath string, global bool) []PinnedError {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PinnedError
	for proj, byID := range s.pins {
		if !global && proj != projectPath {
			continue
		}
		for _, p := range byID {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PinnedAt.After(out[j].PinnedAt) })
	return out
}
