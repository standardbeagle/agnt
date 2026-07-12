package testenv_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"github.com/standardbeagle/agnt/internal/sshclient/testenv"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// This file covers task 08a's SFTP push (sshclient.PushToInbox) at the
// testenv in-process-harness tier.
//
// testenv.Server (server.go) handles only "exec" session requests — it has
// no "subsystem" (sftp) support, which PushToInbox requires. Per the same
// out-of-scope constraint documented in forward_test.go, this file defines
// its own minimal SFTP-subsystem-capable server rather than editing
// server.go, mirroring the shape of internal/sshclient/sftp_test.go's
// sftpFixtureHandler (a real github.com/pkg/sftp server rooted at a temp
// dir) but built standalone here since that helper is unexported to its own
// package.

func sftpServerConfig(t *testing.T, auth *testenv.Auth) *ssh.ServerConfig {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostKey, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return auth.ServerConfig(hostKey)
}

// startSFTPServer starts a minimal in-process SSH server that serves only
// the "sftp" subsystem, real github.com/pkg/sftp.Server semantics, rooted at
// the process's actual filesystem (relative to whatever cwd the SFTP client
// requests) — same "the fixture only supplies transport" approach the
// sshclient package's own SFTP fixture uses.
func startSFTPServer(t *testing.T, auth *testenv.Auth) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg := sftpServerConfig(t, auth)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go handleSFTPConn(conn, cfg)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String()
}

func handleSFTPConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, sessionRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go handleSFTPSession(channel, sessionRequests)
	}
}

func handleSFTPSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	req, ok := <-requests
	if !ok {
		return
	}
	if req.Type != "subsystem" {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}
	var payload struct{ Subsystem string }
	if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Subsystem != "sftp" {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}
	if req.WantReply {
		_ = req.Reply(true, nil)
	}
	go ssh.DiscardRequests(requests)
	srv, err := sftp.NewServer(channel)
	if err != nil {
		return
	}
	_ = srv.Serve()
	srv.Close()
}

// dialSFTPClient dials the SFTP server via sshclient.Dial (a real ssh_config
// + known_hosts round trip, matching the exported entry point) and opens the
// real SFTP subsystem via sshclient.NewSFTPClient.
func dialSFTPClient(t *testing.T, auth *testenv.Auth, addr string) *sftp.Client {
	t.Helper()
	dir := t.TempDir()
	identity := filepath.Join(dir, "id_sftp")
	require.NoError(t, os.WriteFile(identity, auth.PrivateKey, 0o600))
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	config := fmt.Sprintf("Host sftp-target\n HostName %s\n Port %s\n User %s\n IdentityFile %s\n",
		host, port, auth.User, identity)
	configPath := filepath.Join(dir, "ssh_config")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))
	client, err := sshclient.Dial("sftp-target", configPath, filepath.Join(dir, "known_hosts"), auth.User,
		sshclient.Prompter{In: strings.NewReader("yes\n"), Out: io.Discard})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	sc, err := sshclient.NewSFTPClient(client.SSH)
	require.NoError(t, err)
	t.Cleanup(func() { sc.Close() })
	return sc
}

func newPushFixture(t *testing.T) (root string, sc *sftp.Client) {
	t.Helper()
	auth, err := testenv.NewAuth("push-user")
	require.NoError(t, err)
	addr := startSFTPServer(t, auth)
	sc = dialSFTPClient(t, auth, addr)
	return t.TempDir(), sc
}

// TestSFTPPush_DefaultDestination pins the happy path: content lands
// byte-for-byte at <root>/.agnt-inbox/<file>.
func TestSFTPPush_DefaultDestination(t *testing.T) {
	root, sc := newPushFixture(t)
	content := []byte("hello from testenv sftp push")

	remotePath, err := sshclient.PushToInbox(sc, root, "", "note.txt", bytes.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, sshclient.DefaultInboxDir, "note.txt"), remotePath)

	got, err := os.ReadFile(remotePath)
	require.NoError(t, err)
	require.Equal(t, content, got)

	entries, err := os.ReadDir(filepath.Join(root, sshclient.DefaultInboxDir))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no leftover temp files after a successful push")
	require.Equal(t, "note.txt", entries[0].Name())
}

// TestSFTPPush_TraversalRejectionMatrix drives the four traversal-guard
// shapes documented in .claude/rules/lessons-ssh-transport.md #12 (parent
// traversal, absolute path, symlink escape) against the REAL validator over
// a real SFTP connection, plus one legitimate nested-path control case that
// must succeed — so the matrix proves both "attacks rejected" and "the
// guard doesn't also reject legitimate paths".
func TestSFTPPush_TraversalRejectionMatrix(t *testing.T) {
	cases := []struct {
		name        string
		destRelPath func(root string) string
		wantErr     bool
	}{
		{
			name:        "legitimate nested path",
			destRelPath: func(string) string { return "assets/img" },
			wantErr:     false,
		},
		{
			name:        "parent traversal ../../",
			destRelPath: func(string) string { return "../../etc" },
			wantErr:     true,
		},
		{
			name:        "absolute path",
			destRelPath: func(string) string { return "/etc" },
			wantErr:     true,
		},
		{
			name: "symlink escape",
			destRelPath: func(root string) string {
				outside := filepath.Join(filepath.Dir(root), fmt.Sprintf("outside-%s", filepath.Base(root)))
				require.NoError(t, os.MkdirAll(outside, 0o755))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
				return "escape"
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, sc := newPushFixture(t)
			destRelPath := tc.destRelPath(root)
			content := []byte("payload-" + tc.name)

			remotePath, err := sshclient.PushToInbox(sc, root, destRelPath, "payload.bin", bytes.NewReader(content))
			if tc.wantErr {
				require.Error(t, err, "expected traversal rejection for destRelPath %q", destRelPath)
				require.Empty(t, remotePath)
				return
			}
			require.NoError(t, err)
			got, readErr := os.ReadFile(remotePath)
			require.NoError(t, readErr)
			require.Equal(t, content, got)
		})
	}
}

// TestSFTPPush_NoClobberRejectsExistingDestination pins
// PushToInboxNoClobber's atomic no-overwrite guarantee: a second push to the
// same final name fails with ErrDestinationExists and the original content
// is left untouched.
func TestSFTPPush_NoClobberRejectsExistingDestination(t *testing.T) {
	root, sc := newPushFixture(t)
	original := []byte("first-write")

	first, err := sshclient.PushToInboxNoClobber(sc, root, "", "claim.txt", bytes.NewReader(original))
	require.NoError(t, err)

	_, err = sshclient.PushToInboxNoClobber(sc, root, "", "claim.txt", bytes.NewReader([]byte("second-write")))
	require.Error(t, err)
	require.ErrorIs(t, err, sshclient.ErrDestinationExists)

	got, err := os.ReadFile(first)
	require.NoError(t, err)
	require.Equal(t, original, got, "no-clobber collision must leave the original content untouched")
}

// TestSFTPPush_AbortedUploadLeavesNoPartialFile exercises the same
// atomic-write invariant bootstrap_upload_test.go pins for UploadFile:
// PushToInbox must never leave a partially-written file at the final path
// when the source read fails partway through.
func TestSFTPPush_AbortedUploadLeavesNoPartialFile(t *testing.T) {
	root, sc := newPushFixture(t)
	src := &abortAfterReader{data: []byte("0123456789abcdef"), n: 4}

	_, err := sshclient.PushToInbox(sc, root, "", "partial.bin", src)
	require.Error(t, err)

	finalPath := filepath.Join(root, sshclient.DefaultInboxDir, "partial.bin")
	_, statErr := os.Stat(finalPath)
	require.True(t, os.IsNotExist(statErr), "final path must not exist after an aborted push")

	entries, readErr := os.ReadDir(filepath.Join(root, sshclient.DefaultInboxDir))
	require.NoError(t, readErr)
	require.Empty(t, entries, "no leftover temp files after an aborted push")
}

// abortAfterReader yields n bytes then a non-EOF error, simulating a dropped
// connection partway through an upload (mirrors bootstrap_upload_test.go's
// errAfterReader).
type abortAfterReader struct {
	data []byte
	n    int
}

var errSimulatedSFTPAbort = fmt.Errorf("testenv: simulated sftp push abort")

func (r *abortAfterReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errSimulatedSFTPAbort
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
