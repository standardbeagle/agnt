package sshclient

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// execFixtureHandler turns a fixtureServer session channel into a REAL
// command executor: each "exec" request's command string runs via `sh -c`
// against cwd (standing in for the remote's $HOME), with the channel wired
// as stdin/stdout/stderr and a genuine exit-status reply. This is how
// bootstrap's tests exercise uname/go-version/mkdir/cat/sha256sum/chmod/mv
// against real coreutils and a real filesystem (t.TempDir()) instead of a
// hand-rolled mock of shell semantics — the fixture only supplies the SSH
// transport; every command it "remotely" runs is a real local process.
//
// extraEnv is appended to the child's inherited environment (e.g. PATH
// prepended with a fake ~/.local/bin so a just-installed "agnt" script
// becomes runnable, proving the install actually took effect).
func execFixtureHandler(t *testing.T, cwd string, extraEnv ...string) func(channel ssh.Channel, requests <-chan *ssh.Request) {
	t.Helper()
	return func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		for req := range requests {
			if req.Type != "exec" {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			var payload struct{ Command string }
			ssh.Unmarshal(req.Payload, &payload)
			if req.WantReply {
				req.Reply(true, nil)
			}

			cmd := exec.Command("sh", "-c", payload.Command)
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(), extraEnv...)
			cmd.Stdin = channel
			cmd.Stdout = channel
			cmd.Stderr = channel.Stderr()

			runErr := cmd.Run()
			exitCode := 0
			if runErr != nil {
				if ee, ok := runErr.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				} else {
					exitCode = 127
				}
			}
			channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exitCode)}))
			return
		}
	}
}

// remoteHomeEnv returns the PATH/HOME env overrides matching a fixture
// whose "remote $HOME" is homeDir — PATH is prepended with $HOME/.local/bin
// so an agnt binary installed there becomes runnable via bare "agnt".
func remoteHomeEnv(homeDir string) []string {
	return []string{
		"HOME=" + homeDir,
		"PATH=" + filepath.Join(homeDir, ".local", "bin") + ":" + os.Getenv("PATH"),
	}
}

// bootstrap's tests reuse forward_test.go's dialFixtureClient helper (same
// package), which already handles identity file + known_hosts plumbing and
// returns a *Client; callers needing the raw *ssh.Client use its .SSH field.
