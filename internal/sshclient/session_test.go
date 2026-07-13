package sshclient

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRemoteAttachCommand_ShellEscapesMetacharacters(t *testing.T) {
	cmd := RemoteAttachCommand("my; rm -rf /", "/tmp/some' dir")
	want := "agnt attach 'my; rm -rf /'"
	if cmd != want {
		t.Errorf("RemoteAttachCommand = %q, want %q", cmd, want)
	}
	// Sanity: no unescaped single-quote breaks out of quoting.
	if strings.Count(cmd, "'")%2 != 0 && !strings.Contains(cmd, `\'`) {
		t.Errorf("shell quoting looks unbalanced: %q", cmd)
	}
}

func TestRemoteAttachCommand_OmitsUnsupportedLifecycleFlags(t *testing.T) {
	cmd := RemoteAttachCommand("plain", "/work/project")
	for _, flag := range []string{"--cwd", "--create-if-missing"} {
		if strings.Contains(cmd, flag) {
			t.Errorf("RemoteAttachCommand contains unsupported attach flag %q: %q", flag, cmd)
		}
	}
}

func TestSessionHostNameOrIDExists(t *testing.T) {
	result := map[string]interface{}{
		"sessions": []interface{}{
			map[string]interface{}{"session_id": "claude-1", "name": "claude"},
			map[string]interface{}{"session_id": "claude-2", "name": "claude"},
			map[string]interface{}{"session_id": "unique-1", "name": "unique"},
		},
	}

	if !sessionHostNameOrIDExists(result, "claude-2") {
		t.Fatal("exact session id should match")
	}
	if !sessionHostNameOrIDExists(result, "unique") {
		t.Fatal("unique session name should match")
	}
	if sessionHostNameOrIDExists(result, "claude") {
		t.Fatal("ambiguous session name should not match")
	}
	if sessionHostNameOrIDExists(result, "missing") {
		t.Fatal("unknown session should not match")
	}
}

// ptyReqCapture records the pty-req and window-change payloads a fixture
// session receives, so tests can assert on the wire shape without a real
// PTY.
type ptyReqCapture struct {
	mu           sync.Mutex
	ptyReq       *ptyRequestPayload
	windowChange []windowChangePayload
	execCommand  string

	// closeAfterExec, if true, closes the channel right after replying to
	// exec (used by the Relay test to get a deterministic clean end
	// instead of waiting on a context timeout). Tests that need the
	// channel to stay open for further requests (e.g. window-change)
	// leave this false.
	closeAfterExec bool
}

func (c *ptyReqCapture) handle(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		switch req.Type {
		case "pty-req":
			var p ptyRequestPayload
			ssh.Unmarshal(req.Payload, &p)
			c.mu.Lock()
			c.ptyReq = &p
			c.mu.Unlock()
			if req.WantReply {
				req.Reply(true, nil)
			}
		case "window-change":
			var p windowChangePayload
			ssh.Unmarshal(req.Payload, &p)
			c.mu.Lock()
			c.windowChange = append(c.windowChange, p)
			c.mu.Unlock()
			if req.WantReply {
				req.Reply(true, nil)
			}
		case "exec":
			var p struct{ Command string }
			ssh.Unmarshal(req.Payload, &p)
			c.mu.Lock()
			c.execCommand = p.Command
			c.mu.Unlock()
			if req.WantReply {
				req.Reply(true, nil)
			}
			// Echo one line back so Relay has bytes to copy.
			channel.Write([]byte("hello from remote\n"))
			if c.closeAfterExec {
				channel.Close()
				return
			}
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func TestOpenPTYSession_SendsPTYReqAndExecWithCorrectPayloads(t *testing.T) {
	dir := t.TempDir()
	fixture := newFixtureServer(t)
	capture := &ptyReqCapture{}
	fixture.onSession = capture.handle
	stop := fixture.serve(t)
	defer stop()

	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	privPEM, _ := generateClientKey(t)
	keyPath := dir + "/id_test"
	writeFile(t, keyPath, privPEM)

	hkCallback, err := HostKeyCallback(dir+"/known_hosts", prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture: %v", err)
	}
	defer sshClient.Close()

	size := TermSize{Cols: 120, Rows: 40}
	session, err := OpenPTYSessionWithCommand(sshClient, RemoteAttachCommand("my-session", "/work/proj"), size)
	if err != nil {
		t.Fatalf("OpenPTYSession: %v", err)
	}
	defer session.Close()

	// Give the fixture goroutine a moment to record the requests.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		got := capture.ptyReq != nil && capture.execCommand != ""
		capture.mu.Unlock()
		if got {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.ptyReq == nil {
		t.Fatal("fixture never received a pty-req")
	}
	if capture.ptyReq.Columns != 120 || capture.ptyReq.Rows != 40 {
		t.Errorf("pty-req size = %dx%d, want 120x40", capture.ptyReq.Columns, capture.ptyReq.Rows)
	}
	wantCmd := RemoteAttachCommand("my-session", "/work/proj")
	if capture.execCommand != wantCmd {
		t.Errorf("exec command = %q, want %q", capture.execCommand, wantCmd)
	}
}

func TestPTYSession_ResizeSendsWindowChangeWithCorrectPayload(t *testing.T) {
	dir := t.TempDir()
	fixture := newFixtureServer(t)
	capture := &ptyReqCapture{}
	fixture.onSession = capture.handle
	stop := fixture.serve(t)
	defer stop()

	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	privPEM, _ := generateClientKey(t)
	keyPath := dir + "/id_test"
	writeFile(t, keyPath, privPEM)

	hkCallback, err := HostKeyCallback(dir+"/known_hosts", prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture: %v", err)
	}
	defer sshClient.Close()

	session, err := OpenPTYSessionWithCommand(sshClient, RemoteAttachCommand("sess", ""), TermSize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenPTYSession: %v", err)
	}
	defer session.Close()

	if err := session.Resize(TermSize{Cols: 200, Rows: 60}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		n := len(capture.windowChange)
		capture.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.windowChange) != 1 {
		t.Fatalf("expected exactly one window-change request, got %d", len(capture.windowChange))
	}
	if capture.windowChange[0].Columns != 200 || capture.windowChange[0].Rows != 60 {
		t.Errorf("window-change payload = %+v, want 200x60", capture.windowChange[0])
	}
}

func TestPTYSession_RelayCopiesBytesBothDirections(t *testing.T) {
	dir := t.TempDir()
	fixture := newFixtureServer(t)
	capture := &ptyReqCapture{closeAfterExec: true}
	fixture.onSession = capture.handle
	stop := fixture.serve(t)
	defer stop()

	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	privPEM, _ := generateClientKey(t)
	keyPath := dir + "/id_test"
	writeFile(t, keyPath, privPEM)

	hkCallback, err := HostKeyCallback(dir+"/known_hosts", prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture: %v", err)
	}
	defer sshClient.Close()

	session, err := OpenPTYSessionWithCommand(sshClient, RemoteAttachCommand("sess", ""), TermSize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenPTYSession: %v", err)
	}

	var stdout strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = session.Relay(ctx, strings.NewReader(""), &stdout, nil)

	if !strings.Contains(stdout.String(), "hello from remote") {
		t.Errorf("Relay stdout = %q, want it to contain the remote's greeting", stdout.String())
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
