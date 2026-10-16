package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
)

// TestListenOverlaySocketRefusesLiveSocket: "stale socket" removal used to be
// unconditional, so a second run with the same --session code unlinked a live
// session's socket and took its browser events. A socket something answers on
// is left alone and the second listener fails loud.
func TestListenOverlaySocketRefusesLiveSocket(t *testing.T) {
	path := filepath.Join(sockdir.Short(t), "o.sock")

	first, err := listenOverlaySocket(path)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer first.Close()
	go func() {
		for {
			c, err := first.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	if second, err := listenOverlaySocket(path); err == nil {
		second.Close()
		t.Fatal("second listener took over a live socket")
	} else if !strings.Contains(err.Error(), "in use by a live session") {
		t.Fatalf("err = %v, want the live-session refusal", err)
	}

	// The first listener must still be reachable at the same path.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("live socket was disturbed: %v", err)
	}
	_ = conn.Close()
}

// TestListenOverlaySocketReclaimsStaleSocket: a leftover socket file nothing
// listens on (a crashed session) is still replaced.
func TestListenOverlaySocketReclaimsStaleSocket(t *testing.T) {
	path := filepath.Join(sockdir.Short(t), "o.sock")

	dead, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("seed listen: %v", err)
	}
	// Close the listener but keep the file, as a killed process would.
	if ul, ok := dead.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	_ = dead.Close()
	if _, err := os.Lstat(path); err != nil {
		// Platform removed it anyway; a plain file is an equally stale path.
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("seed stale path: %v", err)
		}
	}

	l, err := listenOverlaySocket(path)
	if err != nil {
		t.Fatalf("listen over a stale socket: %v", err)
	}
	defer l.Close()

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat socket: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("socket mode = %o, want 600 regardless of umask", perm)
		}
	}
}

// TestOverlayEndpointEmptyUntilListening: the daemon must never be handed the
// path of a socket this session is not serving.
func TestOverlayEndpointEmptyUntilListening(t *testing.T) {
	path := filepath.Join(sockdir.Short(t), "o.sock")

	holder, err := listenOverlaySocket(path)
	if err != nil {
		t.Fatalf("holder listen: %v", err)
	}
	defer holder.Close()
	go func() {
		for {
			c, err := holder.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	o := newOverlay(path, nil)
	if got := o.Endpoint(); got != "" {
		t.Fatalf("Endpoint before Start = %q, want empty", got)
	}
	if err := o.Start(t.Context()); err == nil {
		o.Stop()
		t.Fatal("Start succeeded over a live socket")
	}
	if got := o.Endpoint(); got != "" {
		t.Fatalf("Endpoint after failed Start = %q, want empty", got)
	}
	// A failed overlay's Stop must not unlink the live session's socket.
	o.Stop()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("failed overlay removed another session's socket: %v", err)
	}

	free := filepath.Join(sockdir.Short(t), "free.sock")
	ok := newOverlay(free, nil)
	if err := ok.Start(t.Context()); err != nil {
		t.Fatalf("Start on a free path: %v", err)
	}
	defer ok.Stop()
	if got := ok.Endpoint(); got != free {
		t.Fatalf("Endpoint = %q, want %q", got, free)
	}
}

// TestOverlaySocketsLiveInPerUserDir: the per-session socket path sits inside
// the per-user directory, so two users' identical session codes cannot meet.
func TestOverlaySocketsLiveInPerUserDir(t *testing.T) {
	prev := overlaySocketPath
	overlaySocketPath = ""
	t.Cleanup(func() { overlaySocketPath = prev })

	got := overlaySessionSocketPath("claude-1")
	if filepath.Dir(got) != overlaySocketDir() {
		t.Fatalf("session socket %q is not inside the per-user dir %q", got, overlaySocketDir())
	}
	if filepath.Dir(overlaySocketDir()) == overlaySocketDir() || filepath.Base(got) != "devtool-overlay-claude-1.sock" {
		t.Fatalf("unexpected socket path %q", got)
	}
}

// TestCompileCustomAlertPatternsReportsBadRegex: an alerts.patterns entry that
// cannot compile used to be dropped with a debug-log line; it is now reported.
func TestCompileCustomAlertPatternsReportsBadRegex(t *testing.T) {
	patterns, problems := compileCustomAlertPatterns(map[string]*config.AlertPatternConfig{
		"good":   {Pattern: `panic: .+`, Severity: "warning"},
		"broken": {Pattern: `(unclosed`},
		"nil":    nil,
	})
	if len(patterns) != 1 || patterns[0].ID != "good" {
		t.Fatalf("patterns = %+v, want only the valid one", patterns)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], `"broken"`) || !strings.Contains(problems[0], "never fire") {
		t.Fatalf("problems = %v, want one naming the broken pattern and its consequence", problems)
	}
}

// TestCompileCustomAlertPatternsWarnAlias: config accepts severity "warn" as
// an alias of "warning", so the scanner must rank it as a warning, not error.
func TestCompileCustomAlertPatternsWarnAlias(t *testing.T) {
	patterns, _ := compileCustomAlertPatterns(map[string]*config.AlertPatternConfig{
		"w": {Pattern: `deprecated`, Severity: "warn"},
	})
	if len(patterns) != 1 || patterns[0].Severity != overlay.AlertSeverityWarning {
		t.Fatalf("patterns = %+v, want one warning-severity pattern", patterns)
	}
}

// TestVersionIsNewer: `agnt upgrade` must not treat "different" as "newer".
func TestVersionIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.17.0", "0.16.0", true},
		{"0.16.1", "0.16.0", true},
		{"1.0.0", "0.99.99", true},
		{"0.16.0", "0.16.0", false},
		{"0.16.0", "0.17.0", false}, // local build ahead of the release: no downgrade
		{"0.9.0", "0.10.0", false},  // numeric, not lexical
		{"v0.17.0", "0.16.0", true},
		{"1.0.0", "1.0.0-rc.1", true}, // the final release supersedes its rc
		{"1.0.0-rc.2", "1.0.0-rc.1", true},
		{"1.0.0-rc.1", "1.0.0", false},
	}
	for _, tc := range cases {
		got, err := versionIsNewer(tc.latest, tc.current)
		if err != nil {
			t.Errorf("versionIsNewer(%q, %q) error: %v", tc.latest, tc.current, err)
			continue
		}
		if got != tc.want {
			t.Errorf("versionIsNewer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
	if _, err := versionIsNewer("not-a-version", "0.16.0"); err == nil {
		t.Error("an unparsable version must be an error, not a guess")
	}
}

// TestCheckUpgradeRunnable: the install step's tools are checked before the
// daemon is stopped.
func TestCheckUpgradeRunnable(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("not found") }
	present := func(name string) (string, error) { return "/usr/bin/" + name, nil }

	for _, m := range []installMethod{installNPM, installPip, installGo, installDirect} {
		if err := checkUpgradeRunnable(m, present); err != nil {
			t.Errorf("%s with tools present: %v", m, err)
		}
		if err := checkUpgradeRunnable(m, missing); err == nil {
			t.Errorf("%s with tools missing: want an error", m)
		}
	}
	if err := checkUpgradeRunnable(installUnknown, present); err == nil {
		t.Error("unknown install method must not be reported runnable")
	}
	if tools, _ := upgradeTools(installDirect, "windows"); len(tools) != 1 || tools[0] != "powershell" {
		t.Errorf("windows direct tools = %v", tools)
	}
	if tools, _ := upgradeTools(installDirect, "linux"); len(tools) != 2 {
		t.Errorf("unix direct tools = %v, want bash and curl", tools)
	}
}
