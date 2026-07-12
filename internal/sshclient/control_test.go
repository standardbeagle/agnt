package sshclient

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withSandboxedHome points os.UserHomeDir (via $HOME) at a fresh t.TempDir
// so control-socket tests never touch the real invoking user's
// ~/.agnt/ssh directory.
func withSandboxedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestListenControl_RegistersSocketAtExpectedPath(t *testing.T) {
	withSandboxedHome(t)

	ln, err := ListenControl("myhost")
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	defer ln.Close()

	wantPath, err := ControlSocketPath("myhost")
	if err != nil {
		t.Fatalf("ControlSocketPath: %v", err)
	}
	if ln.Addr().String() != wantPath {
		t.Errorf("listener addr = %q, want %q", ln.Addr().String(), wantPath)
	}
	if info, statErr := os.Lstat(wantPath); statErr != nil {
		t.Fatalf("stat control socket: %v", statErr)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("control socket mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestListenControl_ReclaimsStaleSocket pins the "crashed agnt ssh process"
// case: a socket FILE left behind (nothing listening on it) must not
// prevent a fresh ListenControl for the same host from succeeding.
func TestListenControl_ReclaimsStaleSocket(t *testing.T) {
	withSandboxedHome(t)

	path, err := ControlSocketPath("stalehost")
	if err != nil {
		t.Fatalf("ControlSocketPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create a listener and then close it WITHOUT removing the socket
	// file, to simulate a process that died without cleanup.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("creating stale socket: %v", err)
	}
	stale.Close() // file remains on disk; nothing is listening now.

	ln, err := ListenControl("stalehost")
	if err != nil {
		t.Fatalf("ListenControl did not reclaim stale socket: %v", err)
	}
	defer ln.Close()
}

// TestDialControl_NoActiveSession pins the Silent Failure Prohibition: with
// no control socket registered for a host, DialControl must return a loud,
// wrapped ErrNoActiveSession rather than a bare/opaque dial error.
func TestDialControl_NoActiveSession(t *testing.T) {
	withSandboxedHome(t)

	_, err := DialControl("nobody-home")
	if err == nil {
		t.Fatal("expected error dialing a host with no active session")
	}
	if !errors.Is(err, ErrNoActiveSession) {
		t.Errorf("error = %v, want wrapping ErrNoActiveSession", err)
	}
	if !strings.Contains(err.Error(), "nobody-home") {
		t.Errorf("error %q does not name the host", err.Error())
	}
}

// startTestControlServer registers and serves a control socket for host
// against a real SFTP fixture rooted at a fresh temp dir, returning the
// project root and a stop func.
func startTestControlServer(t *testing.T, host string) (root string, stop func()) {
	t.Helper()
	root, sc := newSFTPFixture(t)

	ln, err := ListenControl(host)
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	go ServeControl(ln, root, sc)
	return root, func() { ln.Close() }
}

func TestDiscoverActiveHosts_FindsRegisteredSession(t *testing.T) {
	withSandboxedHome(t)
	_, stop := startTestControlServer(t, "active-host")
	defer stop()

	hosts, err := DiscoverActiveHosts()
	if err != nil {
		t.Fatalf("DiscoverActiveHosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "active-host" {
		t.Errorf("hosts = %v, want [active-host]", hosts)
	}
}

// TestDiscoverActiveHosts_SkipsAndRemovesStaleSocket pins the "crashed
// process" cleanup on the discovery side: a socket file with nothing
// listening must be excluded from the result, not surfaced as a false
// positive, and should be removed so it doesn't linger.
func TestDiscoverActiveHosts_SkipsAndRemovesStaleSocket(t *testing.T) {
	home := withSandboxedHome(t)
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stalePath := filepath.Join(dir, "dead-host.ctl")
	stale, err := net.Listen("unix", stalePath)
	if err != nil {
		t.Fatalf("creating stale socket: %v", err)
	}
	stale.Close()

	hosts, err := DiscoverActiveHosts()
	if err != nil {
		t.Fatalf("DiscoverActiveHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("hosts = %v, want none (stale socket should be skipped)", hosts)
	}
	if _, statErr := os.Lstat(stalePath); !os.IsNotExist(statErr) {
		t.Errorf("stale socket file still present after discovery (stat err = %v)", statErr)
	}
}

func TestPushOneFile_RoundTrip(t *testing.T) {
	withSandboxedHome(t)
	root, stop := startTestControlServer(t, "push-target")
	defer stop()

	content := []byte("push round trip payload")
	remotePath, err := PushOneFile("push-target", "note.txt", "", int64(len(content)), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("PushOneFile: %v", err)
	}

	wantPath := filepath.Join(root, DefaultInboxDir, "note.txt")
	if remotePath != wantPath {
		t.Errorf("remotePath = %q, want %q", remotePath, wantPath)
	}
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("reading pushed file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestPushOneFile_NotifiesOnlyAfterReadableUpload(t *testing.T) {
	withSandboxedHome(t)
	root, sc := newSFTPFixture(t)
	ln, err := ListenControl("push-notify")
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	defer ln.Close()

	noticed := make(chan string, 1)
	go ServeControl(ln, root, sc, func(remotePath string, size int64) error {
		content, readErr := os.ReadFile(remotePath)
		if readErr != nil {
			return readErr
		}
		if size != int64(len(content)) {
			return errors.New("notice size did not match readable upload")
		}
		noticed <- string(content)
		return nil
	})

	content := []byte("scripted fixture can read me")
	_, err = PushOneFile("push-notify", "fixture.txt", "", int64(len(content)), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("PushOneFile: %v", err)
	}
	select {
	case got := <-noticed:
		if got != string(content) {
			t.Fatalf("notifier read %q, want %q", got, content)
		}
	case <-time.After(time.Second):
		t.Fatal("file-arrival notifier was not called")
	}
}

func TestPushOneFile_NoticeFailureIsLoud(t *testing.T) {
	withSandboxedHome(t)
	root, sc := newSFTPFixture(t)
	ln, err := ListenControl("push-notice-fail")
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	defer ln.Close()
	go ServeControl(ln, root, sc, func(string, int64) error { return errors.New("no attached session") })

	content := []byte("uploaded despite notice failure")
	_, err = PushOneFile("push-notice-fail", "fixture.txt", "", int64(len(content)), bytes.NewReader(content))
	if err == nil || !strings.Contains(err.Error(), "notice failed") {
		t.Fatalf("error = %v, want loud notice failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, DefaultInboxDir, "fixture.txt")); statErr != nil {
		t.Fatalf("upload should remain committed: %v", statErr)
	}
}

func TestPushOneFile_TraversalRejectedOverTheWire(t *testing.T) {
	withSandboxedHome(t)
	root, stop := startTestControlServer(t, "push-guard")
	defer stop()

	content := []byte("x")
	_, err := PushOneFile("push-guard", "passwd", "../../etc", int64(len(content)), bytes.NewReader(content))
	if err == nil {
		t.Fatal("expected traversal rejection over the control-socket protocol")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "etc", "passwd")); statErr == nil {
		t.Fatal("traversal write escaped project root")
	}
}

func TestPingControl_TimesOutAgainstUnresponsivePeer(t *testing.T) {
	// Regression guard for the liveness-probe lesson
	// (.claude/rules/lessons-liveness-probes.md): pingControl must not
	// block forever against a peer that accepts the connection but never
	// answers.
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "silent.ctl"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(5 * time.Second) // never replies within the test's patience
	}()

	conn, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, pingErr := pingControl(conn)
		done <- pingErr
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ping to fail against a silent peer, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pingControl did not honor its own read deadline")
	}
}
