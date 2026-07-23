package shims

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// BinDirName is the project-relative directory holding shim scripts.
const BinDirName = ".agnt/bin"

// BinDir returns the shim directory for a project.
func BinDir(projectPath string) string {
	return filepath.Join(projectPath, BinDirName)
}

// Ensure installs (or refreshes) shim scripts for the project and returns
// the bin dir to prepend to PATH. It is idempotent: scripts whose content
// already matches are left untouched, and files in the bin dir without the
// agnt shim marker are never modified or removed.
//
// Ensure returns ("", nil) when shims are disabled for the project — the
// caller treats an empty dir as "do not touch PATH".
func Ensure(projectPath string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("shims: empty project path")
	}
	if config.FindAgntConfigFile(projectPath) == "" {
		// No .agnt.kdl in this exact directory: the project is not
		// onboarded, so writing .agnt/bin would be litter.
		return "", nil
	}
	cfg, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		// Unparseable config must not break shell startup; fail open.
		debug.Log("shims", "config load failed for %s: %v (shims skipped)", projectPath, err)
		return "", nil
	}
	if cfg == nil || !cfg.ShimsEnabled() {
		return "", nil
	}

	agntPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("shims: resolve agnt executable: %w", err)
	}
	if agntPath, err = filepath.EvalSymlinks(agntPath); err != nil {
		return "", fmt.Errorf("shims: resolve agnt executable: %w", err)
	}

	dir := BinDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("shims: mkdir %s: %w", dir, err)
	}

	for _, name := range CommandNames() {
		for _, f := range scriptFiles(dir, name, agntPath) {
			if err := writeShimFile(f.path, f.content, f.mode); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

// shimFile is one generated wrapper (path, content, mode).
type shimFile struct {
	path    string
	content string
	mode    os.FileMode
}

// writeShimFile writes content to path unless the existing file already has
// identical content. A file without the shim marker is user content and is
// left alone (reported at debug level).
func writeShimFile(path, content string, mode os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err == nil {
		if string(existing) == content {
			return nil
		}
		if !strings.Contains(string(existing), shimMarker) {
			debug.Log("shims", "not overwriting unmarked file %s", path)
			return nil
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return fmt.Errorf("shims: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("shims: rename %s: %w", path, err)
	}
	return nil
}

// Remove deletes the project's shim dir. Only files carrying the shim
// marker are removed; if user files remain, the directory itself is kept.
// Missing dir is a no-op. Returns true when the dir is gone afterwards.
func Remove(projectPath string) bool {
	dir := BinDir(projectPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	remaining := false
	for _, e := range entries {
		if e.IsDir() {
			remaining = true
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(data), shimMarker) {
			remaining = true
			continue
		}
		if err := os.Remove(p); err != nil {
			remaining = true
		}
	}
	if !remaining {
		if err := os.Remove(dir); err != nil {
			return false
		}
		// Best-effort prune of .agnt if now empty; keep it if other state
		// (store, audit, ...) lives there.
		_ = os.Remove(filepath.Dir(dir))
		return true
	}
	return false
}
