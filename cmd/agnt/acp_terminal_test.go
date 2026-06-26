//go:build unix

package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
)

func TestAcpTerminalWriteTruncation(t *testing.T) {
	// limit chosen so the front gets dropped; payload is multibyte so the
	// rune-boundary trim is exercised.
	tm := &acpTerminal{limit: 10}
	// "héllo" is 6 bytes (é = 2 bytes); write enough to overflow.
	for i := 0; i < 5; i++ {
		if _, err := tm.Write([]byte("héllo")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	out, truncated, exit := tm.snapshot()
	if !truncated {
		t.Fatalf("expected truncated=true after overflow")
	}
	if exit != nil {
		t.Fatalf("expected nil exit status while running")
	}
	if len(out) > tm.limit {
		t.Fatalf("retained %d bytes exceeds limit %d", len(out), tm.limit)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("retained output is not valid UTF-8 (rune boundary not honored): %q", out)
	}
}

func TestAcpTerminalWriteUnderLimit(t *testing.T) {
	tm := &acpTerminal{limit: 1024}
	_, _ = tm.Write([]byte("hello "))
	_, _ = tm.Write([]byte("world"))
	out, truncated, _ := tm.snapshot()
	if truncated {
		t.Fatalf("did not expect truncation under limit")
	}
	if out != "hello world" {
		t.Fatalf("got %q want %q", out, "hello world")
	}
}

func TestAcpTerminalSnapshotExit(t *testing.T) {
	tm := &acpTerminal{limit: 1024}
	_, _ = tm.Write([]byte("done\n"))
	code := 3
	tm.exited = true
	tm.exitCode = &code

	out, _, exit := tm.snapshot()
	if out != "done\n" {
		t.Fatalf("output: got %q", out)
	}
	if exit == nil {
		t.Fatalf("expected exit status once exited")
	}
	if exit.ExitCode == nil || *exit.ExitCode != 3 {
		t.Fatalf("exit code: got %v want 3", exit.ExitCode)
	}
	if exit.Signal != nil {
		t.Fatalf("expected nil signal for normal exit")
	}
}

func TestTerminalExitInfoNil(t *testing.T) {
	code, sig := terminalExitInfo(nil)
	if code != nil || sig != nil {
		t.Fatalf("nil ProcessState should yield (nil,nil); got (%v,%v)", code, sig)
	}
}

// TestGetTerminalUnknown verifies an unknown id is a loud error, not a panic.
func TestGetTerminalUnknown(t *testing.T) {
	c := newACPClient(true)
	if _, err := c.getTerminal("term-404"); err == nil {
		t.Fatalf("expected error for unknown terminal id")
	} else if !strings.Contains(err.Error(), "unknown terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Sanity: the client satisfies the ACP Client interface (compile-time guard
// also exists in acp_client.go).
var _ acp.Client = (*acpClient)(nil)
