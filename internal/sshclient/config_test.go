package sshclient

import (
	"strings"
	"testing"
)

func TestResolveHostFromReader_BasicDirectives(t *testing.T) {
	cfg := `
Host myserver
    HostName 10.0.0.5
    User deploy
    Port 2222
    IdentityFile ~/.ssh/id_deploy
`
	hc, err := ResolveHostFromReader(strings.NewReader(cfg), "myserver")
	if err != nil {
		t.Fatalf("ResolveHostFromReader: %v", err)
	}
	if hc.HostName != "10.0.0.5" {
		t.Errorf("HostName = %q, want 10.0.0.5", hc.HostName)
	}
	if hc.User != "deploy" {
		t.Errorf("User = %q, want deploy", hc.User)
	}
	if hc.Port != 2222 {
		t.Errorf("Port = %d, want 2222", hc.Port)
	}
	if len(hc.IdentityFile) != 1 || !strings.HasSuffix(hc.IdentityFile[0], "/.ssh/id_deploy") {
		t.Errorf("IdentityFile = %v, want one entry ending in /.ssh/id_deploy", hc.IdentityFile)
	}
}

func TestResolveHostFromReader_DefaultPortAndHostName(t *testing.T) {
	hc, err := ResolveHostFromReader(strings.NewReader(""), "plain-host")
	if err != nil {
		t.Fatalf("ResolveHostFromReader: %v", err)
	}
	if hc.Port != 22 {
		t.Errorf("Port = %d, want default 22", hc.Port)
	}
	if hc.HostName != "plain-host" {
		t.Errorf("HostName = %q, want alias fallback plain-host", hc.HostName)
	}
}

func TestResolveHostFromReader_ProxyJumpMultiHop(t *testing.T) {
	cfg := `
Host target
    HostName target.internal
    ProxyJump bastion,jump2:2222
`
	hc, err := ResolveHostFromReader(strings.NewReader(cfg), "target")
	if err != nil {
		t.Fatalf("ResolveHostFromReader: %v", err)
	}
	if hc.ProxyJump != "bastion,jump2:2222" {
		t.Errorf("ProxyJump = %q, want %q", hc.ProxyJump, "bastion,jump2:2222")
	}
}

func TestResolveHostFromReader_NoMatchingBlock(t *testing.T) {
	cfg := `
Host other
    HostName 10.0.0.9
`
	hc, err := ResolveHostFromReader(strings.NewReader(cfg), "unmatched")
	if err != nil {
		t.Fatalf("ResolveHostFromReader: %v", err)
	}
	if hc.HostName != "unmatched" {
		t.Errorf("HostName = %q, want fallback to alias", hc.HostName)
	}
	if hc.Port != 22 {
		t.Errorf("Port = %d, want default 22", hc.Port)
	}
}

func TestResolveHostFromReader_GlobPattern(t *testing.T) {
	cfg := `
Host *.example.com
    User globuser
`
	hc, err := ResolveHostFromReader(strings.NewReader(cfg), "foo.example.com")
	if err != nil {
		t.Fatalf("ResolveHostFromReader: %v", err)
	}
	if hc.User != "globuser" {
		t.Errorf("User = %q, want globuser via glob match", hc.User)
	}
}

func TestResolveHostFromReader_FirstMatchWinsPerDirective(t *testing.T) {
	cfg := `
Host myserver
    User first

Host myserver
    User second
    Port 3333
`
	hc, err := ResolveHostFromReader(strings.NewReader(cfg), "myserver")
	if err != nil {
		t.Fatalf("ResolveHostFromReader: %v", err)
	}
	if hc.User != "first" {
		t.Errorf("User = %q, want first block's value to win", hc.User)
	}
	if hc.Port != 3333 {
		t.Errorf("Port = %d, want second block's Port to fill gap left by first", hc.Port)
	}
}

func TestResolveHost_MissingFileIsNotAnError(t *testing.T) {
	hc, err := ResolveHost("/nonexistent/path/ssh_config_does_not_exist", "somehost")
	if err != nil {
		t.Fatalf("ResolveHost with missing file: %v", err)
	}
	if hc.HostName != "somehost" || hc.Port != 22 {
		t.Errorf("ResolveHost with missing file = %+v, want defaults", hc)
	}
}
