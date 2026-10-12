//go:build unix

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Signal-death classification is Unix-only behaviour: on Windows a terminated
// child reports a plain exit status (there is no "signal: killed" wait state
// and os.Process.Signal(SIGTERM) is unsupported), so these cases cannot be
// produced there. The exit-status cases in shutdown_report_test.go run on
// every OS.

// TestClassifyChildExit_Signal: killed by SIGKILL → unexpected, reason hints
// OOM/resource.
func TestClassifyChildExit_Signal(t *testing.T) {
	c := helperProcess(t, "sleep")
	if err := c.Start(); err != nil {
		t.Fatalf("start sleep helper: %v", err)
	}
	_ = c.Process.Kill()
	killErr := c.Wait()
	un, kind, reason := classifyChildExit(killErr, false)
	if !un || kind != exitSignal || !(strings.Contains(reason, "killed") || strings.Contains(reason, "SIGKILL")) {
		t.Fatalf("SIGKILL: unexpected=%v reason=%q", un, reason)
	}
}

// TestReportUnexpectedShutdown_SignalDoesNotBlameTheAgent covers the case that
// exposed the misattribution: kimi was terminated by a signal from outside, and
// the banner claimed the (redraw) output below was an error kimi reported.
func TestReportUnexpectedShutdown_SignalDoesNotBlameTheAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AGNT_ERROR_LOG", filepath.Join(t.TempDir(), "errors.log"))

	var buf bytes.Buffer
	orig := shutdownWriter
	shutdownWriter = &buf
	t.Cleanup(func() { shutdownWriter = orig })

	c := helperProcess(t, "sleep")
	require.NoError(t, c.Start())
	require.NoError(t, c.Process.Signal(syscall.SIGTERM))
	waitErr := c.Wait()

	kimi := agentLaunch{
		Command:  "kimi",
		Args:     []string{"--agent-file", "/tmp/x/agent.yaml"},
		Injected: []string{"--agent-file", "/tmp/x/agent.yaml"},
		Adapter:  "kimi-cli",
	}
	reportUnexpectedShutdown(waitErr, false, kimi, []string{"⠼ working..."}, nil)
	out := buf.String()

	assert.Contains(t, out, "A signal killed kimi from the outside")
	assert.NotContains(t, out, "from kimi itself", "a signal death is not the agent's error")
	assert.Contains(t, out, "session-cleanup record", "the sender is the question worth answering")
	// Injection guidance belongs to a non-zero exit: a flag agnt appended
	// cannot cause an external SIGTERM, so offering it here is a false lead.
	assert.NotContains(t, out, "agnt appended")
	assert.NotContains(t, out, "disabled true")
}
