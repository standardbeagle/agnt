package tunnel

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// feedChunks writes s to w in small pieces, so a line straddles many reads.
func feedChunks(w io.Writer, s string, size int) {
	for len(s) > 0 {
		n := size
		if n > len(s) {
			n = len(s)
		}
		_, _ = io.WriteString(w, s[:n])
		s = s[n:]
	}
}

// An over-long line is discarded whole and reading carries on. The split is
// the test: the long line arrives in chunks across many reads, and neither its
// head nor its tail may surface as a line of its own.
func TestForEachLine_DiscardsOverLongLineAndKeepsDraining(t *testing.T) {
	long := strings.Repeat("x", maxTunnelLineBytes) + "TAIL-OF-LONG-LINE https://tail.trycloudflare.com"
	input := "first\n" + long + "\n" + "after https://real-one.trycloudflare.com\n" + "last-without-newline"

	pr, pw := io.Pipe()
	wrote := make(chan struct{})
	go func() {
		feedChunks(pw, input, 1000)
		_ = pw.Close()
		close(wrote)
	}()

	var lines []string
	done := make(chan struct{})
	go func() {
		forEachLine(pr, func(line string) { lines = append(lines, line) })
		close(done)
	}()

	// The writer finishing is the drain: an unread pipe would block it.
	select {
	case <-wrote:
	case <-time.After(10 * time.Second):
		t.Fatal("the reader stopped draining: the writer is blocked on the pipe")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("forEachLine did not return at end of stream")
	}

	want := []string{"first", "after https://real-one.trycloudflare.com", "last-without-newline"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %q, want %q", len(lines), abbreviate(lines), want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, abbreviate(lines[i:i+1]), want[i])
		}
	}
}

func abbreviate(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) > 80 {
			l = l[:80] + "…"
		}
		out[i] = l
	}
	return out
}

// A line exactly at the cap is still a line.
func TestForEachLine_LineAtTheCapIsKept(t *testing.T) {
	atCap := strings.Repeat("y", maxTunnelLineBytes)
	var got []string
	forEachLine(strings.NewReader(atCap+"\nnext\n"), func(line string) { got = append(got, line) })
	if len(got) != 2 || got[0] != atCap || got[1] != "next" {
		t.Fatalf("got %d lines (first len %d), want the cap-length line and %q", len(got), len(got[0]), "next")
	}
}

// The quick-tunnel URL is still found after an over-long log line, and the
// URL-shaped tail of that line is not mistaken for it.
func TestParseCloudflareOutput_SurvivesOverLongLine(t *testing.T) {
	tun := New(Config{Provider: ProviderCloudflare, LocalPort: 3000})
	tun.setState(StateStarting)
	long := strings.Repeat("x", maxTunnelLineBytes+1) + " https://tail-of-long.trycloudflare.com"
	tun.parseCloudflareOutput(strings.NewReader(long + "\nINF |  https://real-one.trycloudflare.com  |\n"))

	if got := tun.PublicURL(); got != "https://real-one.trycloudflare.com" {
		t.Fatalf("PublicURL = %q, want the URL from the line after the over-long one", got)
	}
	if tun.State() != StateConnected {
		t.Fatalf("state = %s, want connected", tun.State())
	}
}

func TestNgrokTarget_HonoursLocalHost(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"", 3000, "3000"},
		{"localhost", 3000, "3000"},
		{"127.0.0.1", 3000, "3000"},
		{"::1", 3000, "3000"},
		{"192.168.1.20", 8080, "192.168.1.20:8080"},
		{"app.internal", 8080, "app.internal:8080"},
		{"fd00::5", 8080, "[fd00::5]:8080"},
	}
	for _, tc := range cases {
		if got := ngrokTarget(tc.host, tc.port); got != tc.want {
			t.Errorf("ngrokTarget(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

// `tailscale serve <port>` can only forward to loopback, so a non-loopback
// LocalHost is refused rather than silently replaced by localhost. The refusal
// must come from that check: the binary resolves (it is this test binary) and
// is never run.
func TestTailscale_RefusesNonLoopbackLocalHost(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tun := New(Config{Provider: ProviderTailscale, LocalHost: "192.168.1.20", LocalPort: 3000, BinaryPath: self})
	err = tun.Start(context.Background())
	if err == nil {
		t.Fatal("a non-loopback local host must be refused for tailscale serve")
	}
	if !strings.Contains(err.Error(), "forwards to localhost only") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if tun.State() != StateFailed {
		t.Fatalf("state = %s, want failed", tun.State())
	}
	select {
	case <-tun.Done():
	default:
		t.Fatal("a refused start must close Done so Stop and WaitForURL do not block")
	}
	tun.procMu.Lock()
	started := tun.cmd != nil
	tun.procMu.Unlock()
	if started {
		t.Fatal("tailscale must not have been started")
	}
}

// Stop on a tunnel whose process has already exited succeeds: the process
// being gone is the outcome, not an error.
func TestStop_AlreadyExitedProcessSucceeds(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A child that exits at once: this test binary, asked to run no tests.
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Dir = t.TempDir()
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper process: %v", err)
	}

	tun := New(Config{Provider: ProviderCloudflare, LocalPort: 3000})
	tun.setState(StateConnected)
	tun.procMu.Lock()
	tun.cmd = cmd
	tun.procMu.Unlock()
	tun.closeDone()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tun.Stop(ctx); err != nil {
		t.Fatalf("Stop on an already-exited tunnel: %v", err)
	}
	if tun.State() != StateStopped {
		t.Fatalf("state = %s, want stopped", tun.State())
	}
}
