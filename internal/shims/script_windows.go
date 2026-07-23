//go:build windows

package shims

import (
	"fmt"
	"path/filepath"
	"strings"
)

// scriptFiles renders the cmd.exe wrapper for one command. Windows shells
// resolve `npm` to npm.cmd on PATH, so the shim must be a .cmd too — a bare
// extensionless file would never run. PATHEXT order means npm.cmd in the
// shim dir beats npm.cmd later on PATH.
func scriptFiles(dir, name, agntPath string) []shimFile {
	content := fmt.Sprintf("@echo off\r\nREM %s — managed by agnt, do not edit\r\n\"%s\" shim exec %s %%*\r\n",
		shimMarker, agntPath, name)
	return []shimFile{{
		path:    filepath.Join(dir, name+".cmd"),
		content: strings.ReplaceAll(content, "/", "\\"),
		mode:    0o755,
	}}
}
