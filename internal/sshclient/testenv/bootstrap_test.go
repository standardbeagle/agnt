package testenv_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/sshclient"
	"github.com/standardbeagle/agnt/internal/sshclient/testenv"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// This file covers task 05's connect-time bootstrap check
// (sshclient.CheckRemoteBinary / InstallRemoteBinary) at the testenv
// in-process-harness tier: missing binary, version mismatch, and cross-arch
// source-selection fallthrough.
//
// Unlike forward_test.go and sftp_push_test.go, testenv.Server's existing
// "exec" session support (server.go's handleSession, running every command
// via the real /bin/sh -c) is exactly what CheckRemoteBinary needs — no
// extra channel type is required, so this file uses testenv.Server directly
// rather than defining a standalone server. Determinism comes from
// replacing PATH (via t.Setenv, restored automatically) with a fixture
// directory containing fake `agnt`/`uname`/`go` scripts: the harness runs
// real commands, so this controls what those commands report without
// depending on whatever happens to be installed on the host running the
// tests.

// bootstrapFixturePATH writes a fixture bin directory containing the given
// named scripts (name -> shell script body, without the shebang line) and
// puts PATH's fixture directory FIRST, ahead of the standard coreutils
// locations (/usr/bin:/bin), for the duration of the test: every
// "agnt"/"uname"/"go" invocation inside testenv.Server's real /bin/sh -c
// resolves to the script this test controls (shadowing anything the host
// happens to have installed under those names), while mkdir/cat/chmod/mv/
// sha256sum — needed by UploadFile's real remote-write pipeline but not
// meaningful to fake — still resolve to the real system binaries.
func bootstrapFixturePATH(t *testing.T, scripts map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		path := filepath.Join(dir, name)
		content := "#!/bin/sh\n" + body + "\n"
		require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

// dialBootstrapClient dials a fresh testenv.Server directly with
// golang.org/x/crypto/ssh (bootstrap.go's functions take a raw *ssh.Client,
// not the sshclient.Client wrapper), matching the auth-matrix-style direct
// dial already used elsewhere in this package for tests that don't need
// ssh_config/known_hosts plumbing.
func dialBootstrapClient(t *testing.T) *ssh.Client {
	t.Helper()
	auth, err := testenv.NewAuth("bootstrap-user")
	require.NoError(t, err)
	server, err := testenv.Start(auth)
	require.NoError(t, err)
	t.Cleanup(func() { server.Close() })

	client, err := ssh.Dial("tcp", server.Addr(), auth.ClientConfig())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// TestBootstrap_MissingBinary pins the "remote has no agnt at all" branch:
// NeedsInstall must be true, the reason must say so, and — since the
// fixture's fake uname reports the same OS/arch as the local options —
// selectSource must prefer SourceLocalCopy over a network/go-toolchain
// install.
func TestBootstrap_MissingBinary(t *testing.T) {
	bootstrapFixturePATH(t, map[string]string{
		"uname": `echo "Linux x86_64"`,
		// No "agnt" script at all: "agnt --version" fails with "not found".
		"go": `exit 1`,
	})
	client := dialBootstrapClient(t)

	decision, err := sshclient.CheckRemoteBinary(client, sshclient.BootstrapOptions{
		LocalVersion:    "1.0.0",
		LocalBinaryPath: os.Args[0],
		LocalGOOS:       "linux",
		LocalGOARCH:     "amd64",
	})
	require.NoError(t, err)
	require.True(t, decision.NeedsInstall)
	require.Contains(t, decision.Reason, "was not found")
	require.Empty(t, decision.RemoteVersion)
	require.Equal(t, "linux", decision.RemoteGOOS)
	require.Equal(t, "amd64", decision.RemoteGOARCH)
	require.Equal(t, sshclient.SourceLocalCopy, decision.Source)
	require.NotEmpty(t, decision.FinalPath)
}

// TestBootstrap_VersionMismatch drives both halves of versionsCompatible:
// an incompatible major/minor triggers an install with the mismatch reason,
// while a same-major.minor patch difference reports no install needed.
func TestBootstrap_VersionMismatch(t *testing.T) {
	t.Run("incompatible major.minor triggers install", func(t *testing.T) {
		bootstrapFixturePATH(t, map[string]string{
			"agnt":  `echo "agnt v0.1.0"`,
			"uname": `echo "Linux x86_64"`,
			"go":    `exit 1`,
		})
		client := dialBootstrapClient(t)

		decision, err := sshclient.CheckRemoteBinary(client, sshclient.BootstrapOptions{
			LocalVersion:    "9.9.9",
			LocalBinaryPath: os.Args[0],
			LocalGOOS:       "linux",
			LocalGOARCH:     "amd64",
		})
		require.NoError(t, err)
		require.True(t, decision.NeedsInstall)
		require.Equal(t, "0.1.0", decision.RemoteVersion)
		require.Contains(t, decision.Reason, "outside the compatibility window")
		require.Equal(t, sshclient.SourceLocalCopy, decision.Source)
	})

	t.Run("same major.minor different patch is compatible", func(t *testing.T) {
		bootstrapFixturePATH(t, map[string]string{
			"agnt":  `echo "agnt v1.2.9"`,
			"uname": `echo "Linux x86_64"`,
			"go":    `exit 1`,
		})
		client := dialBootstrapClient(t)

		decision, err := sshclient.CheckRemoteBinary(client, sshclient.BootstrapOptions{
			LocalVersion:    "1.2.3",
			LocalBinaryPath: os.Args[0],
			LocalGOOS:       "linux",
			LocalGOARCH:     "amd64",
		})
		require.NoError(t, err)
		require.False(t, decision.NeedsInstall)
		require.Equal(t, "1.2.9", decision.RemoteVersion)
		require.Contains(t, decision.Reason, "within the compatibility window")
	})
}

// TestBootstrap_CrossArchFallthrough pins selectSource's precedence when the
// local binary can't simply be copied (remote arch differs): prefer
// SourceGoInstall when the remote has a Go toolchain, otherwise fall
// through to SourceReleaseDownload.
func TestBootstrap_CrossArchFallthrough(t *testing.T) {
	t.Run("remote has go toolchain: SourceGoInstall", func(t *testing.T) {
		bootstrapFixturePATH(t, map[string]string{
			// No "agnt" script: forces the NeedsInstall path so selectSource
			// actually runs (a compatible remote never reaches it).
			"uname": `echo "Linux aarch64"`,
			"go":    `echo "go version go1.22.0 linux/arm64"`,
		})
		client := dialBootstrapClient(t)

		decision, err := sshclient.CheckRemoteBinary(client, sshclient.BootstrapOptions{
			LocalVersion:    "1.0.0",
			LocalBinaryPath: os.Args[0],
			LocalGOOS:       "linux",
			LocalGOARCH:     "amd64", // mismatches the fixture's arm64 uname.
		})
		require.NoError(t, err)
		require.True(t, decision.NeedsInstall)
		require.Equal(t, "arm64", decision.RemoteGOARCH)
		require.Equal(t, sshclient.SourceGoInstall, decision.Source)
	})

	t.Run("remote has no go toolchain: SourceReleaseDownload", func(t *testing.T) {
		bootstrapFixturePATH(t, map[string]string{
			"uname": `echo "Linux aarch64"`,
			"go":    `exit 127`, // "go: command failed" — no toolchain remote-side.
		})
		client := dialBootstrapClient(t)

		decision, err := sshclient.CheckRemoteBinary(client, sshclient.BootstrapOptions{
			LocalVersion:    "1.0.0",
			LocalBinaryPath: os.Args[0],
			LocalGOOS:       "linux",
			LocalGOARCH:     "amd64",
		})
		require.NoError(t, err)
		require.True(t, decision.NeedsInstall)
		require.Equal(t, "arm64", decision.RemoteGOARCH)
		require.Equal(t, sshclient.SourceReleaseDownload, decision.Source)
	})
}

// stdinExecServer is a minimal in-process SSH server whose "exec" handling
// wires the SSH channel to the spawned command's Stdin as well as its
// Stdout/Stderr. testenv.Server (server.go's handleSession) does NOT do
// this — it sets cmd.Stdout/cmd.Stderr but leaves cmd.Stdin unset, so any
// exec command reading from stdin gets immediate EOF. That is invisible to
// RemoteAgntVersion/RemoteUname/RemoteHasGo (stdout-only), which is why
// every other test in this file uses testenv.Server directly, but it is
// fatal to UploadFile's "mkdir -p ... && cat > tmpPath" install pattern:
// with no stdin, "cat" writes an empty temp file and the integrity check
// (sha256 of what was sent vs. sha256 of the empty file that landed)
// correctly fails, misreporting a working upload as corrupted.
//
// This is a genuine testenv.Server capability gap (documented instead of
// silently patched — server.go is out of this task's declared 3-file
// scope); this type is this file's local workaround, following the same
// pattern forward_test.go and sftp_push_test.go already used for their own
// missing channel-type support.
type stdinExecServer struct {
	listener net.Listener
	auth     *testenv.Auth
}

func startStdinExecServer(t *testing.T, auth *testenv.Auth) *stdinExecServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg := sftpServerConfig(t, auth)
	s := &stdinExecServer{listener: listener, auth: auth}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go s.handleConn(conn, cfg)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return s
}

func (s *stdinExecServer) Addr() string { return s.listener.Addr().String() }

func (s *stdinExecServer) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
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
		channel, sessionRequests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			continue
		}
		go handleStdinExecSession(channel, sessionRequests)
	}
}

func handleStdinExecSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		_ = ssh.Unmarshal(req.Payload, &payload)
		if req.WantReply {
			_ = req.Reply(true, nil)
		}
		cmd := exec.Command("/bin/sh", "-c", payload.Command)
		cmd.Stdin = channel
		cmd.Stdout = channel
		cmd.Stderr = channel.Stderr()
		runErr := cmd.Run()
		exitCode := uint32(0)
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = uint32(exitErr.ExitCode())
		} else if runErr != nil {
			exitCode = 127
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitCode}))
		return
	}
}

// TestBootstrap_InstallLocalCopy_UploadsAndActivates drives
// InstallRemoteBinary end to end for the SourceLocalCopy branch (missing
// binary, matching OS/arch), confirming the decision actually results in a
// working, correctly-permissioned remote binary at FinalPath — not just the
// right Source classification. Uses stdinExecServer (see its doc comment)
// rather than testenv.Server, since UploadFile's remote write depends on
// stdin being wired through.
func TestBootstrap_InstallLocalCopy_UploadsAndActivates(t *testing.T) {
	remoteHome := t.TempDir()
	bootstrapFixturePATH(t, map[string]string{
		"uname": `echo "Linux x86_64"`,
		"go":    `exit 1`,
	})
	t.Setenv("HOME", remoteHome)

	auth, err := testenv.NewAuth("bootstrap-install-user")
	require.NoError(t, err)
	server := startStdinExecServer(t, auth)
	client, err := ssh.Dial("tcp", server.Addr(), auth.ClientConfig())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	localBinary := filepath.Join(t.TempDir(), "fake-agnt")
	content := []byte("#!/bin/sh\necho fake-agnt\n")
	require.NoError(t, os.WriteFile(localBinary, content, 0o644))

	opts := sshclient.BootstrapOptions{
		LocalVersion:    "1.0.0",
		LocalBinaryPath: localBinary,
		LocalGOOS:       "linux",
		LocalGOARCH:     "amd64",
	}
	decision, err := sshclient.CheckRemoteBinary(client, opts)
	require.NoError(t, err)
	require.True(t, decision.NeedsInstall)
	require.Equal(t, sshclient.SourceLocalCopy, decision.Source)
	require.Equal(t, filepath.Join(remoteHome, ".local", "bin", "agnt"), decision.FinalPath)

	require.NoError(t, sshclient.InstallRemoteBinary(client, opts, decision))

	got, err := os.ReadFile(decision.FinalPath)
	require.NoError(t, err)
	require.Equal(t, content, got)
	info, err := os.Stat(decision.FinalPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

// TestBootstrap_UnsupportedRemoteOS pins RemoteUname's loud failure when
// uname itself is unavailable/fails remote-side (this project's only signal
// for "the remote does not look like Linux/macOS" — see
// ErrWindowsRemoteUnsupported's doc comment).
func TestBootstrap_UnsupportedRemoteOS(t *testing.T) {
	bootstrapFixturePATH(t, map[string]string{
		"uname": `exit 1`,
	})
	client := dialBootstrapClient(t)

	_, err := sshclient.CheckRemoteBinary(client, sshclient.BootstrapOptions{
		LocalVersion:    "1.0.0",
		LocalBinaryPath: os.Args[0],
		LocalGOOS:       "linux",
		LocalGOARCH:     "amd64",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, sshclient.ErrWindowsRemoteUnsupported)
}
