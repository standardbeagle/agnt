//go:build sshe2e

package testenv

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestContainerSSHDConnectAndExec(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fixture, err := StartContainerSSHD(ctx)
	if errors.Is(err, ErrContainerRuntimeUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())
	client, err := ssh.Dial("tcp", fixture.Addr(), fixture.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.CombinedOutput("printf container-ok")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "container-ok" {
		t.Fatalf("output = %q", output)
	}
}
