package sshclient

import (
	"strings"
	"testing"
)

func TestWindowsPipeContract_HostIndependent(t *testing.T) {
	a, err := windowsPipeName("forward", "a:b")
	if err != nil {
		t.Fatal(err)
	}
	b, err := windowsPipeName("forward", "a@b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("sanitized host identities collided")
	}
	if !strings.HasPrefix(strings.ToLower(a), `\\.\pipe\agnt-ssh-forward-`) {
		t.Fatalf("unexpected pipe %q", a)
	}
	if _, err := windowsPipeName("control", " "); err == nil {
		t.Fatal("empty host did not fail loudly")
	}
	if err := validateWindowsPipePath(`C:\tmp\agnt.sock`); err == nil {
		t.Fatal("filesystem path accepted")
	}
	if windowsPipeOwnerOnlySDDL != "D:P(A;;GA;;;OW)" {
		t.Fatalf("ACL widened: %q", windowsPipeOwnerOnlySDDL)
	}
}
