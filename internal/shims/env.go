package shims

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// pathListSep is the PATH entry separator for the current platform.
func pathListSep() string {
	if runtime.GOOS == "windows" {
		return ";"
	}
	return ":"
}

// PrependPATH returns env with dir prepended to PATH. If PATH is absent a
// new entry is appended. An empty dir returns env unchanged.
func PrependPATH(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	prefix := "PATH="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + dir + pathListSep() + strings.TrimPrefix(kv, prefix)
			return env
		}
	}
	return append(env, prefix+dir)
}

// ResolveRealBinary finds the real executable for cmd by scanning PATH and
// skipping any entry inside a shim bin dir (otherwise we'd exec ourselves).
// Returns "" when nothing is found — the caller then reports command-not-
// found exactly like the shell would.
func ResolveRealBinary(cmd string, env []string) string {
	pathEnv := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathEnv = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	if pathEnv == "" {
		pathEnv = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || isShimDir(dir) {
			continue
		}
		if p := lookInDir(dir, cmd); p != "" {
			return p
		}
	}
	return ""
}

// isShimDir reports whether dir is an agnt shim bin dir (…/.agnt/bin).
func isShimDir(dir string) bool {
	clean := filepath.Clean(dir)
	return filepath.Base(clean) == "bin" && filepath.Base(filepath.Dir(clean)) == ".agnt"
}

// lookInDir mirrors exec.LookPath's dir-scoped probe, including the Windows
// PATHEXT extensions a shell would resolve for `npm` (npm.cmd etc).
func lookInDir(dir, cmd string) string {
	candidates := []string{cmd}
	if runtime.GOOS == "windows" {
		candidates = []string{cmd + ".exe", cmd + ".cmd", cmd + ".bat", cmd}
	}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		// Windows has no executable bit; presence + PATHEXT extension is
		// the executable test there.
		if runtime.GOOS == "windows" || info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
