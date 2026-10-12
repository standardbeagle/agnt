package main

import (
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"
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
