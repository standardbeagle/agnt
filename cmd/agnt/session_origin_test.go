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
	"github.com/standardbeagle/agnt/internal/protocol"
	"golang.org/x/sys/unix"
)

func TestSessionSocketOrigin(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/tmp/agnt/devtool-mcp.sock", "local"},
		{"/run/user/1000/agnt/ssh-devbox.sock", "remote:devbox"},
		{"/tmp/agnt/ssh-user@example.sock", "remote:user@example"},
	} {
		if got := sessionSocketOrigin(tc.path); got != tc.want {
			t.Errorf("sessionSocketOrigin(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSessionCommandOriginUsesForwardedSocketFlag(t *testing.T) {
	root := &cobra.Command{Use: "fixture"}
	root.PersistentFlags().String("socket", "", "")
	cmd := &cobra.Command{Use: "hosts"}
	root.AddCommand(cmd)
	if err := root.PersistentFlags().Set("socket", "/tmp/agnt/ssh-remote-dev.sock"); err != nil {
		t.Fatal(err)
	}
	if got := sessionCommandOrigin(cmd); got != "remote:remote-dev" {
		t.Fatalf("command origin = %q, want remote:remote-dev", got)
	}
}

func TestAttachTerminalTitleIncludesSession(t *testing.T) {
	if got := attachTerminalTitle("worker"); got != "\x1b]0;agnt attach · worker\x07" {
		t.Fatalf("attach title = %q", got)
	}
}

func TestAttachAndSessionHostsCommandsExposeRemoteSurface(t *testing.T) {
	project := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "ssh-fixture-host.sock")
	_ = daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: socketPath})
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
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
	attachDone := make(chan error, 1)
	go func() { attachDone <- runAttach(attach, []string{"surface-session"}) }()
	// The title is emitted before the relay enters raw mode. Observe the real
	// slave termios transition so the chord cannot land in canonical buffering.
	rawDeadline := time.Now().Add(5 * time.Second)
	for {
		state, stateErr := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		if state.Lflag&unix.ICANON == 0 {
			break
		}
		if time.Now().After(rawDeadline) {
			t.Fatal("actual attach never entered raw mode")
		}
		time.Sleep(time.Millisecond)
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

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
