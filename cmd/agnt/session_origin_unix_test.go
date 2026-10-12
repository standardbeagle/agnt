//go:build unix

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// This test attaches to a daemon-owned session-host PTY running `sh -c cat`
// and drives it through a real pty pair. Session-host PTYs and `agnt attach`
// are unsupported on native Windows (attach registers a loud stub there), so
// the behaviour under test exists only on Unix.
func TestAttachAndSessionHostsCommandsExposeRemoteSurface(t *testing.T) {
	project := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "ssh-fixture-host.sock")
	_ = daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: socketPath})
	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	created, err := client.SessionHostCreate(protocol.SessionHostCreateConfig{
		Name: "surface-session", ProjectPath: project, Command: "sh", Args: []string{"-c", "cat"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.SessionHostKill(created.SessionID) })

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	root := &cobra.Command{Use: "fixture"}
	root.PersistentFlags().String("socket", socketPath, "")
	attach := &cobra.Command{Use: "attach"}
	hosts := &cobra.Command{Use: "hosts"}
	root.AddCommand(attach, hosts)

	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = tty.Close() })
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = tty, tty
	t.Cleanup(func() { os.Stdin, os.Stdout = oldStdin, oldStdout })
	rawEntered := make(chan struct{})
	attachRawModeEntered = func() { close(rawEntered) }
	t.Cleanup(func() { attachRawModeEntered = nil })
	attachDone := make(chan error, 1)
	go func() { attachDone <- runAttach(attach, []string{"surface-session"}) }()
	select {
	case <-rawEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual attach never entered raw mode")
	}
	if _, err := master.Write(defaultDetachChord); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-attachDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actual PTY attach did not detach")
	}
	os.Stdin, os.Stdout = oldStdin, oldStdout
	_ = tty.Close()
	attachBytes, _ := io.ReadAll(master)
	attachOutput := string(attachBytes)
	if !strings.Contains(attachOutput, "\x1b]0;agnt attach · surface-session\x07") {
		t.Fatalf("actual attach output missing title: %q", attachOutput)
	}

	hostsOutput := captureStdout(t, func() { runSessionHosts(hosts, nil) })
	if !strings.Contains(hostsOutput, "ORIGIN") || !strings.Contains(hostsOutput, "remote:fixture-host") {
		t.Fatalf("actual session hosts output missing forwarded origin:\n%s", hostsOutput)
	}
}
