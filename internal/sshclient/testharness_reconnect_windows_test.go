//go:build windows

package sshclient

import (
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

// These test seams make the platform boundary explicit. The production
// reconnect code is portable, but its hard-close fixture depends on a Unix
// listener/process model and its black-hole fixture depends on sshd plus
// SIGSTOP/SIGCONT. Windows package tests keep compiling and skip only those
// two fixture modes; all platform-neutral reconnect tests remain enabled.
var ErrSSHDNotFound = errors.New("sshclient: Unix sshd freeze harness unavailable on Windows")

type HardCloseHarness struct{}

func NewHardCloseHarness(t *testing.T) *HardCloseHarness {
	t.Helper()
	t.Skip("Unix hard-close SSH harness is unavailable on Windows")
	return nil
}

func (*HardCloseHarness) Addr() string { return "" }
func (*HardCloseHarness) Drop()        {}
func (*HardCloseHarness) Stop()        {}

type SSHDFreezeHarness struct{}

func NewSSHDFreezeHarness(t *testing.T, _ ssh.PublicKey) (*SSHDFreezeHarness, error) {
	t.Helper()
	return nil, ErrSSHDNotFound
}

func (*SSHDFreezeHarness) Addr() string  { return "" }
func (*SSHDFreezeHarness) Freeze() error { return ErrSSHDNotFound }
func (*SSHDFreezeHarness) Resume() error { return ErrSSHDNotFound }
func (*SSHDFreezeHarness) Kill() error   { return nil }
