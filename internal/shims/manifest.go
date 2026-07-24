package shims

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manifest tracks every project with an installed shim bin dir so cleanup
// (graceful Stop, startup sweep, external watcher) can find and remove them
// without scanning the filesystem. The daemon is the sole writer; the
// watcher process is read-only.
type Manifest struct {
	// Projects maps an absolute project path to its shim record.
	Projects map[string]*ManifestEntry `json:"projects"`
	// WatcherPID is the PID of the running `agnt shim watch` process, or 0.
	// Used to avoid spawning duplicates and to reap a stale watcher.
	WatcherPID int `json:"watcher_pid,omitempty"`
	// WatcherBirth is the platform process-birth identity token of the
	// watcher (platform.ProcessBirthID). PID alone is unsafe for liveness
	// after a crash: the kernel recycles PIDs, and a bare kill(pid, 0)
	// would then treat an unrelated process as our watcher. Empty when the
	// platform cannot compute a birth token (check falls back to PID-only).
	WatcherBirth string `json:"watcher_birth,omitempty"`
}

// ManifestEntry is one project's shim installation record.
type ManifestEntry struct {
	BinDir      string    `json:"bin_dir"`
	Commands    []string  `json:"commands"`
	InstalledAt time.Time `json:"installed_at"`
	// Sessions holds the live session codes that depend on this bin dir.
	// The entry is removed when the last session ends.
	Sessions []string `json:"sessions,omitempty"`
}

// manifestPath returns the manifest location under the user state dir,
// honoring XDG_STATE_HOME with a ~/.local/state fallback (same convention
// as the firstrun marker).
func manifestPath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			stateHome = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(stateHome, "agnt", "shims", "manifest.json")
}

// manifestMu serializes manifest mutations within this process. Cross-
// process safety comes from the daemon being the only writer.
var manifestMu sync.Mutex

// LoadManifest reads the manifest. A missing or corrupt file yields an
// empty manifest — cleanup treats "no record" as "nothing to do".
func LoadManifest() *Manifest {
	return loadManifestFrom(manifestPath())
}

func loadManifestFrom(path string) *Manifest {
	m := &Manifest{Projects: map[string]*ManifestEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, m); err != nil || m.Projects == nil {
		return &Manifest{Projects: map[string]*ManifestEntry{}}
	}
	return m
}

// SaveManifest persists the manifest atomically (temp + rename).
func SaveManifest(m *Manifest) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	return saveManifestTo(manifestPath(), m)
}

func saveManifestTo(path string, m *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("shims: manifest dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("shims: manifest write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("shims: manifest rename: %w", err)
	}
	return nil
}

// WithManifest atomically loads, mutates, and saves the manifest.
func WithManifest(fn func(*Manifest)) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m := loadManifestFrom(manifestPath())
	fn(m)
	return saveManifestTo(manifestPath(), m)
}

// RecordInstall upserts the project entry and attaches the session code
// (empty sessionCode is allowed — e.g. agnt init installs without a
// session).
func RecordInstall(projectPath, binDir, sessionCode string) error {
	return WithManifest(func(m *Manifest) {
		e, ok := m.Projects[projectPath]
		if !ok {
			e = &ManifestEntry{BinDir: binDir}
			m.Projects[projectPath] = e
		}
		e.BinDir = binDir
		e.Commands = CommandNames()
		e.InstalledAt = time.Now()
		if sessionCode != "" && !containsString(e.Sessions, sessionCode) {
			e.Sessions = append(e.Sessions, sessionCode)
		}
	})
}

// ReleaseSession detaches a session from its project entry and reports
// whether the project still has sessions (false = bin dir may be removed).
// The entry itself is KEPT even when its session list empties — only
// DropProject removes it, after the bin dir is actually deleted. Deleting
// here would lose crash-recovery track of an installed dir whenever a
// session ends while others (registered but never recorded, e.g. ACP
// terminals) still depend on it.
//
// On a persistence error the session detach may not have been saved, so
// stillUsed is reported as true: the fail-safe default is "keep the bin
// dir", never "delete a dir that may still be in use".
func ReleaseSession(projectPath, sessionCode string) (stillUsed bool, err error) {
	stillUsed = true
	err = WithManifest(func(m *Manifest) {
		e, ok := m.Projects[projectPath]
		if !ok {
			return
		}
		e.Sessions = removeString(e.Sessions, sessionCode)
		stillUsed = len(e.Sessions) > 0
	})
	return stillUsed, err
}

// DropProject removes the entry outright (bin dir already deleted).
func DropProject(projectPath string) error {
	return WithManifest(func(m *Manifest) {
		delete(m.Projects, projectPath)
	})
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(xs []string, s string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
