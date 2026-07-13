//go:build windows

package sshclient

import (
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

// This seam is only for the black-hole fixture, which depends on a real sshd
// subprocess plus SIGSTOP/SIGCONT. The portable hard-close harness and all
// platform-neutral reconnect tests remain enabled on Windows.
var ErrSSHDNotFound = errors.New("sshclient: Unix sshd freeze harness unavailable on Windows")

type SSHDFreezeHarness struct{}

func NewSSHDFreezeHarness(t *testing.T, _ ssh.PublicKey) (*SSHDFreezeHarness, error) {
	t.Helper()
	return nil, ErrSSHDNotFound
}

func (*SSHDFreezeHarness) Addr() string  { return "" }
func (*SSHDFreezeHarness) Freeze() error { return ErrSSHDNotFound }
func (*SSHDFreezeHarness) Resume() error { return ErrSSHDNotFound }
func (*SSHDFreezeHarness) Kill() error   { return nil }
