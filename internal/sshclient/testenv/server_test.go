package testenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestServerConnectAndExec(t *testing.T) {
	auth, err := NewAuth("tester")
	if err != nil {
		t.Fatal(err)
	}
	server, err := Start(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := ssh.Dial("tcp", server.Addr(), auth.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.CombinedOutput("printf in-process-ok")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "in-process-ok" {
		t.Fatalf("output = %q", output)
	}
}

func TestDiscoverMappedPortFallsBackToInspect(t *testing.T) {
	runtime := filepath.Join(t.TempDir(), "runtime")
	script := `#!/bin/sh
if [ "$1" = "port" ]; then
  echo 'Error response from daemon: page not found' >&2
  exit 1
fi
if [ "$1" = "inspect" ]; then
  echo '{"2222/tcp":[{"HostIp":"0.0.0.0","HostPort":"49152"}]}'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(runtime, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	addr, err := discoverMappedPort(context.Background(), runtime, "fixture", "2222/tcp")
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:49152" {
		t.Fatalf("address = %q", addr)
	}
}

func TestValidateContainerOverrides(t *testing.T) {
	if err := validateContainerOverrides("", "tester"); err == nil {
		t.Fatal("SSH_E2E_USER without SSH_E2E_IMAGE must fail")
	}
	if err := validateContainerOverrides("fixture", "tester"); err != nil {
		t.Fatalf("paired conventional overrides rejected: %v", err)
	}
	if err := validateContainerOverrides("linuxserver-compatible", ""); err != nil {
		t.Fatalf("compatible image override rejected: %v", err)
	}
}

func TestServerFaultInjection(t *testing.T) {
	auth, err := NewAuth("tester")
	if err != nil {
		t.Fatal(err)
	}
	server, err := Start(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := ssh.Dial("tcp", server.Addr(), auth.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	server.Freeze()
	reply := make(chan error, 1)
	go func() {
		_, _, requestErr := client.SendRequest("keepalive@openssh.com", true, nil)
		reply <- requestErr
	}()
	select {
	case err := <-reply:
		t.Fatalf("frozen server replied early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	server.Resume()
	select {
	case err := <-reply:
		if err != nil {
			t.Fatalf("resumed request failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resumed server did not answer")
	}

	server.Drop()
	dropped := make(chan error, 1)
	go func() {
		_, _, requestErr := client.SendRequest("keepalive@openssh.com", true, nil)
		dropped <- requestErr
	}()
	select {
	case err := <-dropped:
		if err == nil {
			t.Fatal("dropped transport unexpectedly remained usable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drop did not unblock the transport")
	}
}
