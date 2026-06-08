package license

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoLicense is returned by Load when no license blob is stored.
var ErrNoLicense = errors.New("license: no license installed")

// statePath returns the per-user license blob path under XDG state, honoring
// XDG_STATE_HOME and falling back to ~/.local/state (then the OS temp dir as a
// last resort) — matching firstRunStatePath / daemon.GetLogPath layout. The
// license is per-user, not per-project, so the path is fixed.
func statePath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "agnt", "license.lk")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "agnt", "license.lk")
}

// Save writes the license blob atomically (temp file + rename) at mode 600.
func Save(blob string) error {
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "license-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.TrimSpace(blob) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Load reads the stored license blob. ErrNoLicense if none is installed.
func Load() (string, error) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoLicense
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Remove deletes the stored license blob. Absent file is not an error.
func Remove() error {
	err := os.Remove(statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
