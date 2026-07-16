//go:build !windows

package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestShouldUseWindowsShell_NonWSL covers the universal non-WSL Unix
// case — the helper must always return false because there is no
// cmd.exe to dispatch to. Runs on every Unix host (including WSL,
// where it is gated by the IsWSL() guard documented inline).
func TestShouldUseWindowsShell_NonWSL(t *testing.T) {
	if IsWSL() {
		t.Skip("non-WSL coverage only — WSL host has its own table-driven test")
	}
	cases := []string{
		"",                       // empty
		"/home/user/proj",        // pure Linux path
		"/mnt/c/Users/foo",       // looks WSL-shaped but we're not on WSL
		"C:\\Users\\foo",         // Windows-style backslashes but no IsWSL
		"scripts\\build.cmd",     // Windows-style relative
		"\\\\server\\share\\foo", // UNC
	}
	for _, p := range cases {
		assert.False(t, ShouldUseWindowsShell(p),
			"non-WSL host must return false for any path; got true for %q", p)
	}
}

// TestShouldUseWindowsShell_WSL_PathClassification is the WSL-only
// behavioural test for the helper. Skipped on non-WSL hosts. Covers
// every classification branch of the helper (DrvFs mount, backslash,
// UNC-like, Linux path, empty, edge cases at the /mnt/<drive>/
// boundary).
func TestShouldUseWindowsShell_WSL_PathClassification(t *testing.T) {
	if !IsWSL() {
		t.Skip("WSL-only behavioural test")
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		// Windows-shaped → true
		{"DrvFs C drive root", "/mnt/c/", true},
		{"DrvFs C drive nested", "/mnt/c/Users/foo/proj", true},
		{"DrvFs D drive", "/mnt/d/code/repo", true},
		{"DrvFs z drive", "/mnt/z/scratch", true},
		{"backslash absolute Windows", "C:\\Users\\foo", true},
		{"backslash relative", "scripts\\build.cmd", true},
		{"UNC share", "\\\\server\\share\\foo", true},
		{"backslash inside otherwise-linux path", "/tmp/a\\b", true},

		// Linux-shaped → false
		{"empty", "", false},
		{"home dir", "/home/user/proj", false},
		{"root", "/", false},
		{"tmp", "/tmp/build", false},
		{"relative linux", "scripts/build.sh", false},

		// /mnt edge cases — only /mnt/<lowercase-drive>/ matches
		{"/mnt itself", "/mnt", false},
		{"/mnt/", "/mnt/", false},
		{"/mnt/foo (not single char)", "/mnt/foo", false},
		{"/mnt/C uppercase drive", "/mnt/C/Users", false},
		{"/mnt/c without trailing slash", "/mnt/c", false},
		{"/mnt/cd two-char drive", "/mnt/cd/foo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldUseWindowsShell(tc.path)
			assert.Equal(t, tc.want, got,
				"ShouldUseWindowsShell(%q) on WSL: want %v, got %v", tc.path, tc.want, got)
		})
	}
}

func TestShouldUseWindowsCommand_ClassifiesExecutableNotArguments(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{`printf '%s\n' hello`, false},
		{`sed 's/\s\+/ /g' file`, false},
		{`bash -c "echo C:\\temp"`, false},
		{`REGEX='foo\\.bar' grep "$REGEX" file`, false},
		{`A=one B='two\\three' /usr/bin/env`, false},
		{`A\=x C:\tools\build.cmd`, false},
		{`'A'=x C:\tools\build.cmd`, false},
		{`A=x C:\tools\build.cmd`, true},
		{`A='quoted value' C:\tools\build.cmd`, true},
		{"A=x; C:\\tools\\build.cmd", false},
		{"A=x\nC:\\tools\\build.cmd", false},
		{"A=x && C:\\tools\\build.cmd", false},
		{"A=x || C:\\tools\\build.cmd", false},
		{"A=x | C:\\tools\\build.cmd", false},
		{"A=x & C:\\tools\\build.cmd", false},
		{`A='x;y' C:\tools\build.cmd`, true},
		{`A=x\;y C:\tools\build.cmd`, true},
		{`A='x|y&z' C:\tools\build.cmd`, true},
		{`./my\ script --flag`, false},
		{`C:\tools\build.cmd --flag`, true},
		{`C:\Program Files\tool.exe --flag`, true},
		{`scripts\build.cmd --flag`, true},
		{`"C:\Program Files\tool.exe" --flag`, true},
		{`'C:\Program Files\tool.exe' --flag`, true},
		{`/mnt/c/tools/build.exe --flag`, true},
		{`/usr/bin/env node script.js`, false},
		{`"`, false},
		{`\`, false},
		{`A=`, false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, shouldUseWindowsCommand(tc.command, true), tc.command)
	}
}
