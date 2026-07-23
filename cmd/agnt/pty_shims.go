package main

import (
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/shims"
)

// shimChildEnv installs the project's shim bin dir (.agnt/bin) and prepends
// it to PATH in env, so dev-server / build / kill commands typed in the
// managed shell route through the daemon. No-op when the project has no
// .agnt.kdl or shims are disabled; fail-open scripts keep the shell usable
// without a daemon. Shared by run.go and run_windows.go.
func shimChildEnv(env []string, projectPath string) []string {
	binDir, err := shims.Ensure(projectPath)
	if err != nil {
		debug.Warn("run", "shim install failed for %s: %v", projectPath, err)
		return env
	}
	if binDir == "" {
		return env
	}
	return shims.PrependPATH(env, binDir)
}
