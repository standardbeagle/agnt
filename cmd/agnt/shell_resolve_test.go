//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateFromTTY detaches a test-spawned process from the controlling
// terminal by giving it its own session (Setsid). Tests here execute
// interactive shells (`bash -ic` via wrapInShell) and the real `agnt run`
// binary — both are terminal-handling code. Run from `go test` inside a
// live terminal session (e.g. an AI agent under `agnt run`), a descendant
// that inherits the controlling tty can job-control it: tcsetpgrp/tcsetattr
// from these children can steal the foreground group or half-apply raw
// mode, suspending the developer's foreground TUI and corrupting the tty.
// A fresh session has no controlling terminal, so that is impossible;
// tty-requiring paths then fail deterministically ("inappropriate ioctl")
// and the tests skip.
func isolateFromTTY(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// writeExecutable writes an executable script via a short-lived /bin/sh
// subprocess instead of os.WriteFile. Writing in-process opens the file
// for write in THIS process, and any concurrent fork (leaked daemon
// goroutines, sibling tests' exec machinery — see the goleak ignore list
// in testmain_test.go) briefly inherits that fd between fork and exec;
// exec'ing the script inside that window fails instantly with ETXTBSY
// ("text file busy") — the TestCommandWithArgs_DirectPATH flake. With the
// write fd owned by an already-exited child process, no fork of the test
// process can ever hold it, so the race is structurally impossible.
func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `cat > "$0" && chmod 755 "$0"`, path)
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "write executable script %s: %s", path, out)
}

// createTestScript creates an executable script in binDir that prints a marker.
func createTestScript(t *testing.T, binDir, name, marker string) {
	t.Helper()
	writeExecutable(t, filepath.Join(binDir, name), "#!/bin/sh\necho "+marker+"\n")
}

// setEnv sets an environment variable and restores it on cleanup.
func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

// unsetEnv unsets an environment variable and restores it on cleanup.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, hadOld := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if hadOld {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestCommandWithArgs_DirectPATH(t *testing.T) {
	// When a command IS in PATH, commandWithArgs should run it directly (no shell wrap).
	binDir := t.TempDir()
	marker := "DIRECT_PATH_MARKER_" + t.Name()
	createTestScript(t, binDir, "test-direct-cmd", marker)

	// Add binDir to PATH
	setEnv(t, "PATH", binDir+":"+os.Getenv("PATH"))

	// Verify it's findable
	_, err := exec.LookPath("test-direct-cmd")
	require.NoError(t, err, "test-direct-cmd should be in PATH")

	cmd := commandWithArgs("test-direct-cmd")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "command should succeed")
	assert.Contains(t, string(out), marker)
}

func TestCommandWithArgs_ShellFallback(t *testing.T) {
	// When a command is NOT in PATH, commandWithArgs should fall back to wrapInShell.
	binDir := t.TempDir()
	marker := "SHELL_FALLBACK_MARKER_" + t.Name()
	createTestScript(t, binDir, "test-shell-cmd", marker)

	// Do NOT add binDir to process PATH — it should not be findable via LookPath
	_, err := exec.LookPath("test-shell-cmd")
	require.Error(t, err, "test-shell-cmd should NOT be in PATH")

	// Set up shell config to add binDir
	homeDir := t.TempDir()
	bashrc := "export PATH=" + binDir + ":$PATH\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".bashrc"), []byte(bashrc), 0644))

	setEnv(t, "HOME", homeDir)
	setEnv(t, "SHELL", "/bin/bash")

	// commandWithArgs should fall back to shell wrap since LookPath fails
	cmd := commandWithArgs("test-shell-cmd")
	// Verify it used the shell (args should contain -ic)
	assert.Contains(t, cmd.Args, "-ic", "should use interactive shell")
}

func TestWrapInShell_RespectsSHELL(t *testing.T) {
	setEnv(t, "SHELL", "/bin/bash")

	cmd := wrapInShell("some-command", "arg1")
	assert.Equal(t, "/bin/bash", cmd.Path)
	assert.Equal(t, []string{"/bin/bash", "-ic", "some-command arg1"}, cmd.Args)
}

func TestWrapInShell_DefaultsToSH(t *testing.T) {
	unsetEnv(t, "SHELL")

	cmd := wrapInShell("some-command", "arg1")
	assert.Equal(t, "/bin/sh", cmd.Args[0])
	assert.Contains(t, cmd.Args, "-ic")
}

func TestShellResolve_BashrcPATH(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	binDir := t.TempDir()
	marker := "BASHRC_PATH_MARKER"
	createTestScript(t, binDir, "test-bashrc-cmd", marker)

	homeDir := t.TempDir()
	bashrc := "export PATH=" + binDir + ":$PATH\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".bashrc"), []byte(bashrc), 0644))

	setEnv(t, "HOME", homeDir)
	setEnv(t, "SHELL", bash)

	// Verify NOT in process PATH
	_, err = exec.LookPath("test-bashrc-cmd")
	require.Error(t, err)

	cmd := wrapInShell("test-bashrc-cmd")
	isolateFromTTY(cmd)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "shell-wrapped command should succeed; output: %s", string(out))
	assert.Contains(t, string(out), marker)
}

func TestShellResolve_BashrcAlias(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	homeDir := t.TempDir()
	marker := "ALIAS_MARKER_" + t.Name()
	// shopt expand_aliases is needed for non-interactive-but-forced-interactive bash
	bashrc := "shopt -s expand_aliases\nalias test-alias-cmd='echo " + marker + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".bashrc"), []byte(bashrc), 0644))

	setEnv(t, "HOME", homeDir)
	setEnv(t, "SHELL", bash)

	cmd := wrapInShell("test-alias-cmd")
	isolateFromTTY(cmd)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "alias command should succeed; output: %s", string(out))
	assert.Contains(t, string(out), marker)
}

func TestShellResolve_ZshrcPATH(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not available")
	}

	binDir := t.TempDir()
	marker := "ZSHRC_PATH_MARKER"
	createTestScript(t, binDir, "test-zshrc-cmd", marker)

	homeDir := t.TempDir()
	// Zsh reads .zshrc in interactive mode
	zshrc := "export PATH=" + binDir + ":$PATH\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte(zshrc), 0644))

	setEnv(t, "HOME", homeDir)
	setEnv(t, "SHELL", zsh)
	// Prevent zsh from reading global configs that might interfere
	setEnv(t, "ZDOTDIR", homeDir)

	_, err = exec.LookPath("test-zshrc-cmd")
	require.Error(t, err)

	cmd := wrapInShell("test-zshrc-cmd")
	isolateFromTTY(cmd)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "zsh shell-wrapped command should succeed; output: %s", string(out))
	assert.Contains(t, string(out), marker)
}

func TestShellResolve_ProfilePATH(t *testing.T) {
	// .profile is read by sh (and bash --login). Use sh -ic to test.
	binDir := t.TempDir()
	marker := "PROFILE_PATH_MARKER"
	createTestScript(t, binDir, "test-profile-cmd", marker)

	homeDir := t.TempDir()
	profile := "export PATH=" + binDir + ":$PATH\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".profile"), []byte(profile), 0644))

	setEnv(t, "HOME", homeDir)
	setEnv(t, "SHELL", "/bin/sh")
	// Unset ENV to prevent sh from sourcing unexpected files
	unsetEnv(t, "ENV")

	_, err := exec.LookPath("test-profile-cmd")
	require.Error(t, err)

	// sh -ic sources .profile on some systems; on others it doesn't.
	// The important thing is wrapInShell constructs the right command.
	cmd := wrapInShell("test-profile-cmd")
	assert.Equal(t, "/bin/sh", cmd.Args[0])
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "test-profile-cmd")
}

func TestShellResolve_E2E_Binary(t *testing.T) {
	// Full E2E: agnt run --no-overlay <cmd> with custom HOME/SHELL.
	// Requires TTY — skips gracefully when not available.
	if f, err := os.Open("/dev/tty"); err != nil {
		t.Skip("test requires TTY - skipping in non-interactive environment")
	} else {
		f.Close()
	}

	agntPath := findAgntBinary(t)

	binDir := t.TempDir()
	marker := "E2E_BINARY_MARKER"
	createTestScript(t, binDir, "test-e2e-cmd", marker)

	homeDir := t.TempDir()
	bashrc := "export PATH=" + binDir + ":$PATH\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".bashrc"), []byte(bashrc), 0644))

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	cmd := exec.Command(agntPath, "run", "--no-overlay", "test-e2e-cmd")
	isolateFromTTY(cmd)
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"SHELL="+bash,
	)

	done := make(chan struct{})
	var output []byte
	go func() {
		output, _ = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-done // drain output after kill
		outStr := string(output)
		if strings.Contains(outStr, "failed to set raw mode") ||
			strings.Contains(outStr, "inappropriate ioctl") {
			t.Skip("test requires TTY - skipping in non-interactive environment")
		}
		t.Fatal("command timed out")
	}

	outStr := string(output)
	if strings.Contains(outStr, "failed to set raw mode") ||
		strings.Contains(outStr, "inappropriate ioctl") {
		t.Skip("test requires TTY - skipping in non-interactive environment")
	}

	assert.Contains(t, outStr, marker, "expected marker in output: %s", outStr)
}

func TestShellResolve_ArgumentPreservation(t *testing.T) {
	// Verify that arguments with spaces and quotes survive shell wrapping.
	binDir := t.TempDir()
	// Script that prints all arguments, one per line
	script := "#!/bin/sh\nfor arg in \"$@\"; do echo \"ARG:$arg\"; done\n"
	writeExecutable(t, filepath.Join(binDir, "test-args-cmd"), script)

	setEnv(t, "PATH", binDir+":"+os.Getenv("PATH"))

	// Test via direct PATH (no shell wrap) — baseline
	cmd := commandWithArgs("test-args-cmd", "hello world", "it's", "--flag=value")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	outStr := string(out)
	assert.Contains(t, outStr, "ARG:hello world")
	assert.Contains(t, outStr, "ARG:it's")
	assert.Contains(t, outStr, "ARG:--flag=value")
}

func TestShellResolve_ArgumentPreservation_ShellWrap(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	binDir := t.TempDir()
	script := "#!/bin/sh\nfor arg in \"$@\"; do echo \"ARG:$arg\"; done\n"
	writeExecutable(t, filepath.Join(binDir, "test-args-wrap"), script)

	homeDir := t.TempDir()
	bashrc := "export PATH=" + binDir + ":$PATH\n"
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".bashrc"), []byte(bashrc), 0644))

	setEnv(t, "HOME", homeDir)
	setEnv(t, "SHELL", bash)

	// Shell-wrapped command with special arguments
	cmd := wrapInShell("test-args-wrap", "hello world", "it's", "--flag=value")
	isolateFromTTY(cmd)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "output: %s", string(out))
	outStr := string(out)
	assert.Contains(t, outStr, "ARG:hello world")
	assert.Contains(t, outStr, "ARG:it's")
	assert.Contains(t, outStr, "ARG:--flag=value")
}
