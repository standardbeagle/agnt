package sshclient

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpFixtureHandler turns a fixtureServer session channel into a REAL SFTP
// server (github.com/pkg/sftp's own sftp.Server) rooted at remoteRoot via
// WithServerWorkingDirectory, plus a real `sh -c` exec handler for
// ResolveRemoteProjectRoot's "cd ... && pwd" probe — mirroring
// execFixtureHandler's "the fixture only supplies transport; every command
// is a real local process" approach (bootstrap_fixture_test.go), extended
// to also serve the "subsystem sftp" request a real SFTP client sends
// instead of "exec".
func sftpFixtureHandler(t *testing.T) func(channel ssh.Channel, requests <-chan *ssh.Request) {
	t.Helper()
	return func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		req, ok := <-requests
		if !ok {
			return
		}
		switch req.Type {
		case "subsystem":
			var payload struct{ Subsystem string }
			ssh.Unmarshal(req.Payload, &payload)
			if payload.Subsystem != "sftp" {
				if req.WantReply {
					req.Reply(false, nil)
				}
				return
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
			go ssh.DiscardRequests(requests)
			srv, err := sftp.NewServer(channel)
			if err != nil {
				return
			}
			srv.Serve()
			srv.Close()
		case "exec":
			runFixtureExec(channel, req)
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// runFixtureExec runs a single already-received "exec" request's command
// via `sh -c` in the current process's working directory, matching
// execFixtureHandler's semantics but operating on a request already pulled
// off the requests channel (sftpFixtureHandler dispatches on the first
// request's type before this is called).
func runFixtureExec(channel ssh.Channel, req *ssh.Request) {
	var payload struct{ Command string }
	ssh.Unmarshal(req.Payload, &payload)
	if req.WantReply {
		req.Reply(true, nil)
	}
	cmd := exec.Command("sh", "-c", payload.Command)
	cmd.Stdin = channel
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		exitCode = 1
	}
	channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exitCode)}))
}

// newSFTPFixture starts a fixture server serving both exec (cwd = root) and
// a real SFTP subsystem, dials it, opens the SFTP client, and returns
// root plus the connected sftp.Client.
func newSFTPFixture(t *testing.T) (root string, sc *sftp.Client) {
	t.Helper()
	root = t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = sftpFixtureHandler(t)
	stop := fixture.serve(t)
	t.Cleanup(stop)

	client := dialFixtureClient(t, fixture)
	sc, err := NewSFTPClient(client.SSH)
	if err != nil {
		t.Fatalf("NewSFTPClient: %v", err)
	}
	t.Cleanup(func() { sc.Close() })
	return root, sc
}

func TestPushToInbox_DefaultDestinationLandsInAgntInbox(t *testing.T) {
	root, sc := newSFTPFixture(t)
	content := []byte("fake png bytes")

	remotePath, err := PushToInbox(sc, root, "", "logo.png", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("PushToInbox: %v", err)
	}

	wantPath := filepath.Join(root, DefaultInboxDir, "logo.png")
	if remotePath != wantPath {
		t.Errorf("remotePath = %q, want %q", remotePath, wantPath)
	}
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("reading pushed file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("pushed content = %q, want %q", got, content)
	}

	entries, err := os.ReadDir(filepath.Join(root, DefaultInboxDir))
	if err != nil {
		t.Fatalf("reading inbox dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "logo.png" {
		t.Errorf("expected exactly one file 'logo.png' in inbox, got %v", entries)
	}
}

func TestPushToInbox_ExplicitDestRelPath(t *testing.T) {
	root, sc := newSFTPFixture(t)
	content := []byte("hello")

	remotePath, err := PushToInbox(sc, root, "assets/img", "logo.png", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("PushToInbox: %v", err)
	}
	wantPath := filepath.Join(root, "assets", "img", "logo.png")
	if remotePath != wantPath {
		t.Errorf("remotePath = %q, want %q", remotePath, wantPath)
	}
}

func TestPushToInbox_RejectsParentTraversal(t *testing.T) {
	root, sc := newSFTPFixture(t)
	_, err := PushToInbox(sc, root, "../../etc", "passwd", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected traversal rejection, got nil error")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "etc", "passwd")); statErr == nil {
		t.Fatal("traversal write succeeded outside project root")
	}
}

func TestPushToInbox_RejectsAbsoluteDestPath(t *testing.T) {
	root, sc := newSFTPFixture(t)
	_, err := PushToInbox(sc, root, "/etc", "passwd", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected absolute-path rejection, got nil error")
	}
}

// TestPushToInbox_RejectsSymlinkEscape pins the traversal guard's third
// check: a destination directory that is itself, on the remote host, a
// symlink resolving outside the project root must be rejected even though
// the requested path string never contains "..".
func TestPushToInbox_RejectsSymlinkEscape(t *testing.T) {
	root, sc := newSFTPFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("creating escape symlink: %v", err)
	}

	_, err := PushToInbox(sc, root, "escape", "payload.txt", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected symlink-escape rejection, got nil error")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "payload.txt")); statErr == nil {
		t.Fatal("symlink escape write landed outside project root")
	}
}

// errAfterReaderSFTP mirrors bootstrap_upload_test.go's errAfterReader:
// yields n bytes then a non-EOF error, simulating a dropped connection or
// local read failure partway through an upload.
type errAfterReaderSFTP struct {
	data []byte
	n    int
	err  error
}

func (r *errAfterReaderSFTP) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, r.err
	}
	take := r.n
	if take > len(p) {
		take = len(p)
	}
	if take > len(r.data) {
		take = len(r.data)
	}
	copy(p, r.data[:take])
	r.data = r.data[take:]
	r.n -= take
	return take, nil
}

var errSimulatedPushAbort = errors.New("simulated push abort")

// TestPushToInbox_AbortedUploadLeavesNoPartialFile pins the atomic-upload
// acceptance criterion: if src errors partway through, PushToInbox must
// return an error and the final path must never come into existence.
func TestPushToInbox_AbortedUploadLeavesNoPartialFile(t *testing.T) {
	root, sc := newSFTPFixture(t)
	src := &errAfterReaderSFTP{data: []byte("0123456789"), n: 5, err: errSimulatedPushAbort}

	_, err := PushToInbox(sc, root, "", "partial.bin", src)
	if err == nil {
		t.Fatal("expected error from aborted upload, got nil")
	}

	finalPath := filepath.Join(root, DefaultInboxDir, "partial.bin")
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("final path exists after aborted upload (stat err = %v)", statErr)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, DefaultInboxDir))
	if readErr != nil {
		t.Fatalf("reading inbox dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("expected no leftover files in inbox dir, got %v", entries)
	}
}

func TestValidateDestRelPath(t *testing.T) {
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"", false},
		{"assets", false},
		{"assets/img", false},
		{"..", true},
		{"../x", true},
		{"a/../../b", true},
		{"/etc", true},
	}
	for _, c := range cases {
		err := validateDestRelPath(c.path)
		if (err != nil) != c.wantErr {
			t.Errorf("validateDestRelPath(%q) error = %v, wantErr %v", c.path, err, c.wantErr)
		}
	}
}

func TestVerifyWithinRoot_RejectsSiblingWithSharedPrefix(t *testing.T) {
	if err := verifyWithinRoot("/home/u/proj", "/home/u/proj-evil/x"); err == nil {
		t.Fatal("expected rejection of sibling directory with shared name prefix")
	}
	if err := verifyWithinRoot("/home/u/proj", "/home/u/proj/x"); err != nil {
		t.Errorf("unexpected rejection of genuine descendant: %v", err)
	}
	if err := verifyWithinRoot("/home/u/proj", "/home/u/proj"); err != nil {
		t.Errorf("unexpected rejection of root itself: %v", err)
	}
}
