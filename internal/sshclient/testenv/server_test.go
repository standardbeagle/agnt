package testenv

import (
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
