//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
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

func TestLocalSSHUser_PreservesUnixAndSupportsNativeWindows(t *testing.T) {
	tests := []struct{ name, user, username, want string }{
		{name: "Unix and WSL prefer USER", user: "unix-user", username: "windows-user", want: "unix-user"},
		{name: "native Windows falls back to USERNAME", username: "windows-user", want: "windows-user"},
		{name: "missing identity remains empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := localSSHUser(func(key string) string {
				if key == "USER" {
					return tc.user
				}
				if key == "USERNAME" {
					return tc.username
				}
				return ""
			})
			if got != tc.want {
				t.Fatalf("localSSHUser = %q, want %q", got, tc.want)
			}
		})
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
	var gotEvent protocol.DeveloperEvent
	owner.project = "/remote/project"
	owner.reportEvent = func(client *daemon.Client, event protocol.DeveloperEvent) error {
		gotClient, gotEvent = client, event
		return nil
	}
	owner.reportPortForward("port 5173 in use locally", []sshclient.Mapping{{
		ProxyID: "recovered-proxy", RemotePort: 5173, LocalPort: 5174, Remapped: true,
	}})

	if gotClient != newest {
		t.Fatalf("recovered callback used stale daemon client %p, want newest %p", gotClient, newest)
	}
	if gotEvent.ProxyID != "recovered-proxy" || gotEvent.ProjectPath != "/remote/project" || !strings.Contains(gotEvent.Message, "5174") {
		t.Fatalf("recovered callback lost current mapping telemetry: event=%+v", gotEvent)
	}
}

func TestReconnectForwarding_DefaultPathCollisionUsesResolvedProjectRoot(t *testing.T) {
	resolved := "/home/fixture/project"
	owner := newReconnectForwardingOwner("fixture", resolved)
	owner.dclient = &daemon.Client{}
	var got protocol.DeveloperEvent
	owner.reportEvent = func(_ *daemon.Client, event protocol.DeveloperEvent) error { got = event; return nil }
	owner.reportPortForward("port 5173 in use locally", []sshclient.Mapping{{ProxyID: "web", RemotePort: 5173, LocalPort: 5174, Remapped: true}})
	if got.ProjectPath != resolved || got.ProxyID != "web" {
		t.Fatalf("default-path collision event = %+v, want resolved project %q", got, resolved)
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
	wrapped := &sshclient.Client{SSH: client}
	remoteSocket := filepath.Join(t.TempDir(), "remote-daemon.sock")
	_ = daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: remoteSocket})
	localSocket := filepath.Join(t.TempDir(), "ssh-fixture-host.sock")
	forwarder, err := sshclient.NewForwarder(wrapped, remoteSocket, localSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = forwarder.Close() })
	go func() { _ = forwarder.Serve() }()
	dclient := daemon.NewClientWithPath(localSocket)
	if err := dclient.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dclient.Close() })
	if _, err := dclient.SessionRegister("ssh-forward-fixture-host", "", "", "agnt ssh", nil); err != nil {
		t.Fatal(err)
	}
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
	created, err := dclient.SessionHostCreate(protocol.SessionHostCreateConfig{Name: "forwarded-session", ProjectPath: project, Command: "sh", Args: []string{"-c", "cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dclient.SessionHostKill(created.SessionID) })
	root := &cobra.Command{Use: "fixture"}
	root.PersistentFlags().String("socket", localSocket, "")
	hosts := &cobra.Command{Use: "hosts"}
	hosts.Flags().Bool("global", true, "")
	root.AddCommand(hosts)
	hostsOutput := captureStdout(t, func() { runSessionHosts(hosts, nil) })
	if !strings.Contains(hostsOutput, "forwarded-session") || !strings.Contains(hostsOutput, "remote:fixture-host") {
		t.Fatalf("forwarded session origin output:\n%s", hostsOutput)
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fixture")) }))
	t.Cleanup(backend.Close)
	proxyResult, err := dclient.ProxyStart("fixture-web", backend.URL, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dclient.ProxyStop("fixture-web") })
	_ = proxyResult
	mgr := sshclient.NewPortForwardManager(wrapped, dclient, func(string) {})
	ownerForPublish := &reconnectForwarding{host: "fixture-host", project: project, dclient: dclient}
	mgr.SetOnChange(ownerForPublish.publishForwardMappings)
	mgr.Start(context.Background())
	t.Cleanup(mgr.Stop)
	<-mgr.Reconciled()
	if len(mgr.Status()) != 1 || mgr.Status()[0].ProxyID != "fixture-web" {
		t.Fatalf("reconciliation-derived mappings = %+v", mgr.Status())
	}
	portsConn := daemon.NewConn(localSocket)
	defer portsConn.Close()
	var inventory struct {
		Ports []struct {
			ProxyID   string `json:"proxy_id"`
			Forwarded bool   `json:"forwarded"`
			LocalPort int    `json:"local_port"`
		} `json:"ports"`
	}
	if err := portsConn.Request(protocol.VerbPorts, protocol.SubVerbQuery).WithJSON(protocol.DirectoryFilter{Global: true}).JSONInto(&inventory); err != nil {
		t.Fatal(err)
	}
	foundForwarded := false
	for _, port := range inventory.Ports {
		if port.ProxyID == "fixture-web" && port.Forwarded && port.LocalPort == mgr.Status()[0].LocalPort {
			foundForwarded = true
		}
	}
	if !foundForwarded {
		t.Fatalf("active reconnectForwarding snapshot absent from PORTS inventory: %+v", inventory.Ports)
	}

	queue := sshclient.NewPushQueue(project, 2, nil, nil)
	events := make(chan string, 4)
	control := &reconnectControl{
		queue: queue, projectRoot: project,
		onPause:   func() { events <- "control-paused" },
		onResume:  func() { events <- "control-resumed" },
		onFlushed: func() { events <- "queue-flushed" },
	}
	control.Pause()
	if got := <-events; got != "control-paused" {
		t.Fatalf("first lifecycle event = %q", got)
	}
	queued := queue.Queued()
	pushDone := make(chan error, 1)
	go func() {
		_, err := queue.Push("queued.txt", "", strings.NewReader("queued"))
		pushDone <- err
	}()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("push did not enter reconnect queue")
	}

	oldStatusFlag := sshShowForwardStatus
	sshShowForwardStatus = true
	t.Cleanup(func() { sshShowForwardStatus = oldStatusFlag })
	owner := &reconnectForwarding{
		status:   sshclient.NewStatusTracker("surface-session"),
		control:  control,
		ports:    mgr,
		onResume: func() { events <- "forward-resumed" },
	}
	var queuedStatus bytes.Buffer
	owner.renderStatusTo(&queuedStatus)
	for _, want := range []string{"connected", "session: surface-session", "queued pushes: 1", "fixture-web", "remote :"} {
		if !strings.Contains(queuedStatus.String(), want) {
			t.Errorf("queued status surface missing %q:\n%s", want, queuedStatus.String())
		}
	}

	mgr.Pause()
	mgr.Resume(context.Background(), wrapped, dclient)
	events <- "forward-resumed"
	forwardReady := mgr.Reconciled()
	queueDrained := control.Resume(wrapped)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForLifecycle(ctx, forwardReady, queueDrained); err != nil {
		t.Fatal(err)
	}
	if got := <-events; got != "forward-resumed" {
		t.Fatalf("resume event = %q, want forward-resumed", got)
	}
	if got := <-events; got != "control-resumed" {
		t.Fatalf("resume event = %q, want control-resumed", got)
	}
	if got := <-events; got != "queue-flushed" {
		t.Fatalf("resume event = %q, want queue-flushed", got)
	}
	if err := <-pushDone; err == nil {
		t.Fatal("fixture SFTP is unsupported; queued push should complete with a surfaced error")
	}
	var finalStatus bytes.Buffer
	owner.renderStatusTo(&finalStatus)
	if !strings.Contains(finalStatus.String(), "queued pushes: 0") || !strings.Contains(finalStatus.String(), "fixture-web") {
		t.Fatalf("final reconciled/drained status:\n%s", finalStatus.String())
	}
	queue.Close()
}

func TestWaitForLifecycleCancellationBreaksStalledGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stalled := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- waitForLifecycle(ctx, stalled) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled lifecycle wait deadlocked")
	}
}

func TestSSHRelayLifecycleCtrlCCancelsStalledReconnect(t *testing.T) {
	inputR, inputW := io.Pipe()
	defer inputR.Close()
	defer inputW.Close()

	ctx, cancel := context.WithCancel(context.Background())
	pump := sshclient.NewInputPump(cancel)
	pump.Start(inputR)

	stalled := make(chan struct{})
	rendered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- completeSSHReconnect(ctx, stalled, stalled, func() { rendered <- struct{}{} })
	}()
	if _, err := inputW.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("relay lifecycle error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ctrl-C did not terminate stalled reconnect lifecycle")
	}
	select {
	case <-rendered:
		t.Fatal("relay rendered/resumed after Ctrl-C cancellation")
	default:
	}
}
