//go:build !windows

package main

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sshclient"
)

func TestParseHostPath(t *testing.T) {
	cases := []struct {
		arg      string
		wantHost string
		wantPath string
	}{
		{"myhost", "myhost", ""},
		{"myhost:/remote/path", "myhost", "/remote/path"},
		{"user@myhost:relative/dir", "user@myhost", "relative/dir"},
		{"myhost:", "myhost", ""},
		// Documented rule: split on the FIRST colon, so a second colon
		// (unusual, but shows the rule is unambiguous) stays in the path.
		{"myhost:/a:b", "myhost", "/a:b"},
	}
	for _, c := range cases {
		host, path := parseHostPath(c.arg)
		if host != c.wantHost || path != c.wantPath {
			t.Errorf("parseHostPath(%q) = (%q, %q), want (%q, %q)", c.arg, host, path, c.wantHost, c.wantPath)
		}
	}
}

func TestReconnectForwarding_RecoveryCallbacksUseNewestDurableDaemonClient(t *testing.T) {
	owner := &reconnectForwarding{host: "fixture"} // initial forwarding failed
	first := &daemon.Client{}
	newest := &daemon.Client{}
	owner.mu.Lock()
	owner.dclient = first // first recovery
	owner.mu.Unlock()
	owner.mu.Lock()
	owner.dclient = newest // subsequent reconnect
	owner.mu.Unlock()

	var gotClient *daemon.Client
	var gotProxyID string
	var gotConfig protocol.ToastConfig
	owner.toast = func(client *daemon.Client, proxyID string, config protocol.ToastConfig) {
		gotClient, gotProxyID, gotConfig = client, proxyID, config
	}
	owner.reportPortForward("port 5173 in use locally", []sshclient.Mapping{{
		ProxyID: "recovered-proxy", RemotePort: 5173, LocalPort: 5174, Remapped: true,
	}})

	if gotClient != newest {
		t.Fatalf("recovered callback used stale daemon client %p, want newest %p", gotClient, newest)
	}
	if gotProxyID != "recovered-proxy" || !strings.Contains(gotConfig.Message, "5174") {
		t.Fatalf("recovered callback lost current mapping telemetry: id=%q config=%+v", gotProxyID, gotConfig)
	}
}

// TestSSHToolFlagRejected pins the drop decision for the inert --tool flag
// (see .claude/rules/lessons-ssh-transport.md item 3, and the tracking epic
// 01KWMARXTVWKC33EPHZZJ43JT9 where remote tool selection will eventually
// land): agnt ssh must not silently accept-and-ignore --tool. The flag
// definition was removed entirely, so cobra's own flag parser rejects it as
// unknown before RunE (and thus before any SSH dial attempt) ever runs.
//
// Execute must be invoked on rootCmd, not sshCmd directly: cobra's
// ExecuteC redirects any command with a parent to its root and uses the
// ROOT's configured args (see cobra's "run on Root only" behavior), so
// SetArgs on the child alone would be silently ignored.
func TestSSHToolFlagRejected(t *testing.T) {
	rootCmd.SetArgs([]string{"ssh", "myhost", "--tool", "claude"})
	defer rootCmd.SetArgs(nil)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error for unknown --tool flag, got nil")
	}
	if !strings.Contains(err.Error(), "tool") {
		t.Fatalf("expected error to mention the rejected flag %q, got: %v", "--tool", err)
	}
}

// TestSSHReconnectFlags_ParseAndBindVars pins that the reconnect-related
// flags (task 09c, spec §3.6/§6 CLI surface) are real, registered flags —
// not parsed-and-ignored (see .claude/rules/lessons-ssh-transport.md item 4
// on Config Authority: parsed-but-unacted flags are bugs) — by parsing them
// directly against sshCmd's own flag set (bypassing Execute/RunE, so this
// never dials anything).
func TestSSHReconnectFlags_ParseAndBindVars(t *testing.T) {
	defer func() {
		sshCreateIfMissing = false
		sshNewSession = false
		sshReconnectMax = 0
	}()

	if err := sshCmd.ParseFlags([]string{"--create-if-missing", "--new", "--reconnect-max", "5"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if !sshCreateIfMissing {
		t.Error("--create-if-missing did not set sshCreateIfMissing")
	}
	if !sshNewSession {
		t.Error("--new did not set sshNewSession")
	}
	if sshReconnectMax != 5 {
		t.Errorf("--reconnect-max = %d, want 5", sshReconnectMax)
	}
}
