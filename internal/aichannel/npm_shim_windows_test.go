//go:build windows

package aichannel

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These tests start real processes under a real ConPTY, so none of them uses
// t.Parallel(). The program started is always this test binary in its "argv"
// helper mode (see helper_process_test.go) — never a real agent CLI.

// multiLinePrompt is the kind of argument cmd.exe cannot carry: newlines, a
// tab, double quotes, and the characters batch treats as syntax.
const multiLinePrompt = "Summarize the status below.\nLine two has \"double quotes\" and a\ttab.\n\nLine four: 100% & <done> | ^caret^ (parens)\nlast line"

// installFakeNode places a copy of the test binary at path, to stand in for
// node.exe or a native agent executable.
func installFakeNode(t *testing.T, path string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating test binary: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A copy, not a hard link: a link shares the running test binary's image,
	// which Windows will not let TempDir cleanup delete.
	src, err := os.Open(self)
	if err != nil {
		t.Fatalf("open test binary: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatalf("copy test binary: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// argvHelperEnv returns the environment that puts the fake node in argv mode,
// and the file it will write its arguments to.
func argvHelperEnv(t *testing.T) (env map[string]string, argvFile string) {
	t.Helper()
	argvFile = filepath.Join(t.TempDir(), "argv.json")
	return map[string]string{helperModeEnv: "argv", helperArgvFileEnv: argvFile}, argvFile
}

func readRecordedArgv(t *testing.T, argvFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("the started program recorded no argv: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatalf("decode recorded argv %q: %v", data, err)
	}
	return argv
}

// startUnderConPTY runs command through the real startPTYCommand path and
// returns once it has exited.
func startUnderConPTY(t *testing.T, command string, args []string, env map[string]string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	sess, err := startPTYCommand(ctx, cmd)
	if err != nil {
		return err
	}
	defer sess.Close()
	out, waitErr, err := collectPTYOutput(sess)
	if err != nil || waitErr != nil {
		t.Fatalf("run under ConPTY: err=%v waitErr=%v output=%q", err, waitErr, out)
	}
	if !strings.Contains(out, "argv-recorded") {
		t.Fatalf("PTY output %q lacks the helper's marker", out)
	}
	return nil
}

// TestConPTYResolvesNpmShim is the regression for the summarizer failing for
// every Windows user whose agent CLI is an npm install: the `.cmd` shim is
// resolved to node + the entry script and started directly, so a multi-line
// prompt arrives as one intact argument.
func TestConPTYResolvesNpmShim(t *testing.T) {
	cases := []struct {
		name    string
		shimDir string // relative to the test's temp dir
		entry   string
		shim    func(entry string) string
	}{
		{"current shim", "npm", `node_modules\@anthropic-ai\claude-code\cli.js`, currentNpmShim},
		{"legacy shim", "npm", `node_modules\pkg\cli.js`, legacyNpmShim},
		{"paths containing spaces", `Program Files\npm global`, `node_modules\my pkg\dist dir\cli.js`, currentNpmShim},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.shimDir)
			shim, script := writeShimFixture(t, dir, "claude", tc.entry, tc.shim(tc.entry))
			installFakeNode(t, filepath.Join(dir, "node.exe"))
			env, argvFile := argvHelperEnv(t)

			args := []string{"--model", "haiku", "-p", multiLinePrompt, "--output-format", "json"}
			if err := startUnderConPTY(t, shim, args, env); err != nil {
				t.Fatalf("startPTYCommand: %v", err)
			}

			want := append([]string{script}, args...)
			if got := readRecordedArgv(t, argvFile); !reflect.DeepEqual(got, want) {
				t.Fatalf("argv = %q\nwant  %q", got, want)
			}
		})
	}
}

// TestConPTYShimUsesNodeFromPATH: with no node.exe beside the shim, the one on
// PATH is used.
func TestConPTYShimUsesNodeFromPATH(t *testing.T) {
	const entry = `node_modules\pkg\cli.js`
	shim, script := writeShimFixture(t, t.TempDir(), "claude", entry, currentNpmShim(entry))
	nodeDir := t.TempDir()
	installFakeNode(t, filepath.Join(nodeDir, "node.exe"))
	t.Setenv("PATH", nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env, argvFile := argvHelperEnv(t)

	if err := startUnderConPTY(t, shim, []string{"-p", multiLinePrompt}, env); err != nil {
		t.Fatalf("startPTYCommand: %v", err)
	}
	want := []string{script, "-p", multiLinePrompt}
	if got := readRecordedArgv(t, argvFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant  %q", got, want)
	}
}

// TestConPTYRefusesUnrecognizedBatchFile: a `.cmd` that is not an npm shim is
// neither guessed at nor handed to cmd.exe with a prompt it would mangle. It
// fails with the reason and the ways out, and nothing is started.
func TestConPTYRefusesUnrecognizedBatchFile(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "claude.cmd")
	if err := os.WriteFile(wrapper, []byte("@echo off\r\nclaude --verbose %*\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installFakeNode(t, filepath.Join(dir, "node.exe")) // present, and must stay unused
	env, argvFile := argvHelperEnv(t)

	err := startUnderConPTY(t, wrapper, []string{"-p", multiLinePrompt}, env)
	if err == nil {
		t.Fatal("an unrecognized batch file was started")
	}
	for _, want := range []string{"not a recognized npm command shim", "was not run", "API mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Fatal("something ran despite the refusal")
	}
}

// TestConPTYStartsNativeExecutable: the non-shim path is unchanged — a native
// `.exe` is started as given and gets the same intact argv.
func TestConPTYStartsNativeExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "native agent", "claude.exe")
	installFakeNode(t, exe)
	env, argvFile := argvHelperEnv(t)

	args := []string{"--system-prompt", "You are terse.\nTwo lines.", "-p", multiLinePrompt}
	if err := startUnderConPTY(t, exe, args, env); err != nil {
		t.Fatalf("startPTYCommand: %v", err)
	}
	if got := readRecordedArgv(t, argvFile); !reflect.DeepEqual(got, args) {
		t.Fatalf("argv = %q\nwant  %q", got, args)
	}
}

// TestClaudeAdapterThroughNpmShim drives the whole CLI path the summarizer
// uses — RunWithAdapter, ClaudeAdapter.BuildCommand, the shim bypass, ConPTY —
// with a multi-line prompt, context and system prompt.
func TestClaudeAdapterThroughNpmShim(t *testing.T) {
	const entry = `node_modules\@anthropic-ai\claude-code\cli.js`
	dir := filepath.Join(t.TempDir(), "npm")
	shim, script := writeShimFixture(t, dir, "claude", entry, currentNpmShim(entry))
	installFakeNode(t, filepath.Join(dir, "node.exe"))
	env, argvFile := argvHelperEnv(t)

	const system = "You summarize dev-server status.\nBe brief."
	const inputContext = "proc dev: running\nproxy dev: 2 errors\n\"quoted\" line"
	cfg := applyDefaults(Config{Agent: AgentClaude, Command: shim, Model: "haiku", Env: env, Timeout: 2 * time.Minute})

	out, err := RunWithAdapter(context.Background(), cfg, multiLinePrompt, inputContext, system)
	if err != nil {
		t.Fatalf("RunWithAdapter: %v", err)
	}
	if !strings.Contains(out, "argv-recorded") {
		t.Fatalf("output %q lacks the helper's marker", out)
	}

	want := []string{
		script,
		"--model", "haiku",
		"--system-prompt", system,
		"-p", "<context>\n" + inputContext + "\n</context>\n\n" + multiLinePrompt,
	}
	if got := readRecordedArgv(t, argvFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant  %q", got, want)
	}
}

// TestPipeModeBypassesNpmShim: the non-PTY adapters put the prompt in argv
// too, so their commands get the same bypass.
func TestPipeModeBypassesNpmShim(t *testing.T) {
	const entry = `node_modules\some-agent\bin\cli.js`
	dir := filepath.Join(t.TempDir(), "npm")
	shim, script := writeShimFixture(t, dir, "agent", entry, currentNpmShim(entry))
	installFakeNode(t, filepath.Join(dir, "node.exe"))
	env, argvFile := argvHelperEnv(t)

	cfg := applyDefaults(Config{Agent: AgentCustom, Command: shim, NonInteractiveFlag: "-p", Env: env, Timeout: 2 * time.Minute})
	if _, err := RunWithAdapter(context.Background(), cfg, multiLinePrompt, "", ""); err != nil {
		t.Fatalf("RunWithAdapter: %v", err)
	}
	want := []string{script, "-p", multiLinePrompt}
	if got := readRecordedArgv(t, argvFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant  %q", got, want)
	}
}
