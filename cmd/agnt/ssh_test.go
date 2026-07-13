//go:build !windows

package main

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"github.com/standardbeagle/agnt/internal/sshclient/testenv"
	"golang.org/x/crypto/ssh"
)

type fakeReversePull struct {
	starts  int
	resumes int
	stops   int
	client  *daemon.Client
	sftp    *sftp.Client
}

func (f *fakeReversePull) Start(context.Context) { f.starts++ }
func (f *fakeReversePull) Resume(_ context.Context, client *daemon.Client, sc *sftp.Client) {
	f.resumes++
	f.client = client
	f.sftp = sc
}
func (f *fakeReversePull) Stop() { f.stops++ }

func TestReconnectForwarding_ReversePullFollowsProductionLifecycle(t *testing.T) {
	originalFactory := newReversePullManager
	defer func() { newReversePullManager = originalFactory }()

	firstDaemon := &daemon.Client{}
	secondDaemon := &daemon.Client{}
	firstSFTP := &sftp.Client{}
	secondSFTP := &sftp.Client{}
	fake := &fakeReversePull{}
	var constructedHost string
	newReversePullManager = func(client *daemon.Client, sc *sftp.Client, host string, _ func(string)) reversePullLifecycle {
		fake.client, fake.sftp, constructedHost = client, sc, host
		return fake
	}

	owner := &reconnectForwarding{host: "remote-alias", dropSFTP: firstSFTP}
	owner.connectPull(firstDaemon)
	if fake.starts != 1 || fake.client != firstDaemon || fake.sftp != firstSFTP || constructedHost != "remote-alias" {
		t.Fatalf("initial live wiring = starts:%d daemon:%p sftp:%p host:%q", fake.starts, fake.client, fake.sftp, constructedHost)
	}

	// Pause is the production disconnect path: the stream must drain before
	// the old SFTP/daemon transports are closed by the owner.
	owner.dropSFTP = nil // zero-value test client cannot be closed
	owner.Pause()
	if fake.stops != 1 {
		t.Fatalf("disconnect stops = %d, want 1", fake.stops)
	}

	owner.dropSFTP = secondSFTP
	owner.connectPull(secondDaemon)
	if fake.resumes != 1 || fake.client != secondDaemon || fake.sftp != secondSFTP {
		t.Fatalf("reconnect wiring = resumes:%d daemon:%p sftp:%p", fake.resumes, fake.client, fake.sftp)
	}

	// Avoid asking Stop to close the zero-value fixture SFTP clients; this
	// assertion isolates ownership of the reverse-pull lifecycle itself.
	owner.dropSFTP = nil
	owner.Stop()
	if fake.stops != 2 {
		t.Fatalf("teardown stops = %d, want 2", fake.stops)
	}
}

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

func TestSSHClientSurfaces_WithRealSSHFixture(t *testing.T) {
	auth, err := testenv.NewAuth("surface-user")
	if err != nil {
		t.Fatal(err)
	}
	server, err := testenv.Start(auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	client, err := ssh.Dial("tcp", server.Addr(), auth.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	project, err := sshclient.ResolveRemoteProjectRoot(client, "")
	if err != nil {
		t.Fatal(err)
	}

	var firstScreen bytes.Buffer
	writeSSHFirstScreen(&firstScreen, "fixture-host", "1.2.3", "1.2.4", project, "surface-session")
	for _, want := range []string{"\x1b]0;agnt ssh fixture-host · surface-session\x07", "local 1.2.3", "remote 1.2.4", "project: " + project, "detach: Ctrl-\\ Ctrl-\\"} {
		if !strings.Contains(firstScreen.String(), want) {
			t.Errorf("first screen missing %q:\n%s", want, firstScreen.String())
		}
	}
	if strings.Contains(firstScreen.String(), "(remote default)") {
		t.Fatalf("first screen used unresolved project placeholder:\n%s", firstScreen.String())
	}

	queue := sshclient.NewPushQueue(project, 2, nil, nil)
	queue.Reconnecting()
	pushDone := make(chan error, 1)
	go func() {
		_, err := queue.Push("queued.txt", "", strings.NewReader("queued"))
		pushDone <- err
	}()
	deadline := time.After(5 * time.Second)
	for queue.Depth() != 1 {
		select {
		case <-deadline:
			t.Fatal("push did not enter reconnect queue")
		default:
			runtime.Gosched()
		}
	}
	t.Cleanup(func() {
		queue.Close()
		<-pushDone
	})

	oldStatusFlag := sshShowForwardStatus
	sshShowForwardStatus = true
	t.Cleanup(func() { sshShowForwardStatus = oldStatusFlag })
	owner := &reconnectForwarding{
		status:  sshclient.NewStatusTracker("surface-session"),
		control: &reconnectControl{queue: queue, projectRoot: project},
	}
	var status bytes.Buffer
	owner.renderStatusTo(&status, []sshclient.Mapping{{ProxyID: "fixture-web", RemotePort: 5173, LocalPort: 5174}})
	for _, want := range []string{"connected", "session: surface-session", "queued pushes: 1", "fixture-web", "remote :5173"} {
		if !strings.Contains(status.String(), want) {
			t.Errorf("status surface missing %q:\n%s", want, status.String())
		}
	}
}
