package main

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemonclient"
)

// TestNewDaemonConfig_ThreadsFeedbackLimits pins the config→daemon seam: the
// operator's feedback{} block (config.Config.Feedback, spec §5 limits) MUST be
// threaded into DaemonConfig.FeedbackLimits so the live public-plane limiter runs
// the configured values, not defaults. Before the wiring fix this dropped the
// config silently and FeedbackLimits stayed zero (buildPublicPlane then reset it
// to spec defaults) — the no-silent-fallback / Config Authority violation.
func TestNewDaemonConfig_ThreadsFeedbackLimits(t *testing.T) {
	appCfg := config.DefaultConfig()
	// Non-default operator values, clearly distinct from spec §5 defaults
	// (10/5/4096/500/90) so a defaults-reset can't masquerade as a pass.
	appCfg.Feedback = config.FeedbackConfig{
		RatePerMinute:   30,
		Burst:           2,
		MaxBodyBytes:    1024,
		MaxRowsPerShare: 123,
		RetentionDays:   7,
	}

	got := newDaemonConfig(appCfg, "/tmp/sock", "127.0.0.1:0")

	if got.FeedbackLimits != appCfg.Feedback {
		t.Errorf("FeedbackLimits not threaded from config: got %+v, want %+v",
			got.FeedbackLimits, appCfg.Feedback)
	}
	if got.PublicListenAddr != "127.0.0.1:0" {
		t.Errorf("PublicListenAddr = %q, want 127.0.0.1:0", got.PublicListenAddr)
	}
}

// TestRunDaemonSocketPath_PrintsDefaultSocketPathOneLine pins the contract
// consumed by 'agnt ssh': exec'd over SSH, its stdout must be exactly the
// socket path, one line, no decoration, so the caller can use it verbatim
// as the remote endpoint for a direct-streamlocal channel.
func TestRunDaemonSocketPath_PrintsDefaultSocketPathOneLine(t *testing.T) {
	want := daemonclient.DefaultSocketPath()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	runDaemonSocketPath(daemonSocketPathCmd, nil)

	w.Close()
	os.Stdout = origStdout

	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line of output, got %d: %v", len(lines), lines)
	}
	if strings.TrimSpace(lines[0]) != want {
		t.Errorf("output = %q, want %q", lines[0], want)
	}
}
