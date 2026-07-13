package main

import "testing"

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

func TestAttachTerminalTitleIncludesSession(t *testing.T) {
	if got := attachTerminalTitle("worker"); got != "\x1b]0;agnt attach · worker\x07" {
		t.Fatalf("attach title = %q", got)
	}
}
