package config

import (
	"runtime"
	"testing"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/assert"
)

// TestResolveShell_ExplicitShellWins covers the highest-priority branch
// — an explicit Shell config must always win, even on WSL with a
// /mnt/c/... cwd. Runs on every platform.
func TestResolveShell_ExplicitShellWins(t *testing.T) {
	s := &ScriptConfig{
		Run:   "echo hi",
		Cwd:   "/mnt/c/Users/foo", // would normally trigger cmd.exe on WSL
		Shell: "bash",
	}
	shell, args := s.ResolveShell()
	assert.Equal(t, "bash", shell)
	assert.Equal(t, []string{"-c", "echo hi"}, args)
}

// TestResolveShell_PlatformDefault_NonWSLUnix confirms the legacy Unix
// default (sh -c) still applies for plain Linux paths on non-WSL hosts.
// Skipped on Windows (which has its own default) and on WSL (covered by
// the WSL-specific test below).
func TestResolveShell_PlatformDefault_NonWSLUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows platform default test")
	}
	if platform.IsWSL() {
		t.Skip("WSL has its own ResolveShell test")
	}
	s := &ScriptConfig{
		Run: "echo hi",
		Cwd: "/home/user/proj",
	}
	shell, args := s.ResolveShell()
	assert.Equal(t, "sh", shell)
	assert.Equal(t, []string{"-c", "echo hi"}, args)
}

// TestResolveShell_NonWSL_WindowsLookingPath_StillSh asserts that a
// path that *looks* WSL-shaped on a non-WSL host does not accidentally
// trigger cmd.exe. The ShouldUseWindowsShell guard is IsWSL()-gated;
// regressions to that gate would silently break every Linux user with
// a project under (say) `/mnt/data/...`. Skipped on Windows.
func TestResolveShell_NonWSL_WindowsLookingPath_StillSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows platform default test")
	}
	if platform.IsWSL() {
		t.Skip("guard checks non-WSL only")
	}
	s := &ScriptConfig{
		Run: "echo hi",
		Cwd: "/mnt/c/Users/foo", // Windows-shaped, but we're not on WSL
	}
	shell, args := s.ResolveShell()
	assert.Equal(t, "sh", shell, "non-WSL host must not switch to cmd.exe based on path shape")
	assert.Equal(t, []string{"-c", "echo hi"}, args)
}

// TestResolveShell_WSL_DrvFsCwdPicksCmd is the headline test for this
// task: a script with a /mnt/c/... cwd on WSL must dispatch via
// cmd.exe /c, not sh -c. Skipped on non-WSL hosts where the branch is
// unreachable.
func TestResolveShell_WSL_DrvFsCwdPicksCmd(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("WSL-only — exercises the Windows-path branch")
	}
	s := &ScriptConfig{
		Run: "build.cmd",
		Cwd: "/mnt/c/Users/foo/proj",
	}
	shell, args := s.ResolveShell()
	assert.Equal(t, "cmd.exe", shell, "WSL + /mnt/<drive>/ cwd must dispatch via cmd.exe")
	assert.Equal(t, []string{"/c", "build.cmd"}, args)
}

// TestResolveShell_WSL_BackslashRunPicksCmd covers the "Cwd is Linux
// but Run is a Windows-style absolute path" case (e.g. user runs an
// installer that lives at `C:\tools\installer.cmd` from a Linux cwd).
// Skipped on non-WSL.
func TestResolveShell_WSL_BackslashRunPicksCmd(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("WSL-only")
	}
	s := &ScriptConfig{
		Run: "C:\\tools\\installer.cmd",
		Cwd: "/home/user", // Linux cwd
	}
	shell, args := s.ResolveShell()
	assert.Equal(t, "cmd.exe", shell, "WSL + backslash in run must dispatch via cmd.exe")
	assert.Equal(t, []string{"/c", "C:\\tools\\installer.cmd"}, args)
}

// TestResolveShell_WSL_LinuxOnlyStaysOnSh asserts that a script whose
// cwd and run are both pure Linux still uses sh on WSL. Skipped on
// non-WSL. Without this guard the helper would over-trigger on every
// WSL session, breaking the common "I'm doing Linux dev under WSL"
// case.
func TestResolveShell_WSL_LinuxOnlyStaysOnSh(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("WSL-only")
	}
	s := &ScriptConfig{
		Run: "go test ./...",
		Cwd: "/home/user/proj",
	}
	shell, args := s.ResolveShell()
	assert.Equal(t, "sh", shell, "WSL + pure Linux paths must stay on sh")
	assert.Equal(t, []string{"-c", "go test ./..."}, args)
}
