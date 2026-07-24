//go:build !windows

package shims

import (
	"fmt"
	"path/filepath"

	"github.com/standardbeagle/agnt/internal/platform"
)

// scriptFiles renders the POSIX shell wrapper for one command. The script
// execs the absolute agnt binary baked at install time so PATH edits inside
// the session cannot shadow the router itself. %q on both paths keeps
// spaces in project/user dirs safe.
func scriptFiles(dir, name, agntPath string) []shimFile {
	content := fmt.Sprintf("#!/bin/sh\n# %s — managed by agnt, do not edit\nexec %s shim exec %s \"$@\"\n",
		shimMarker, platform.ShellQuote(agntPath), platform.ShellQuote(name))
	return []shimFile{{
		path:    filepath.Join(dir, name),
		content: content,
		mode:    0o755,
	}}
}
