package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBuildKeySequenceShiftOnlyUppercasesLetters(t *testing.T) {
	o := &Overlay{}
	cases := map[string]string{"a": "A", "z": "Z", "1": "1", "-": "-", "A": "A", ";": ";"}
	for key, want := range cases {
		if got := o.buildKeySequence(KeyMessage{Key: key, Shift: true}); got != want {
			t.Errorf("Shift+%q = %q, want %q", key, got, want)
		}
	}
}

func TestTruncateHookMsgRuneSafe(t *testing.T) {
	s := strings.Repeat("é", 50)
	got := truncateHookMsg(s, 11)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated message is invalid UTF-8: %q", got)
	}
	if len(got) > 11 {
		t.Fatalf("len %d exceeds max", len(got))
	}
	if truncateHookMsg("short", 10) != "short" {
		t.Fatal("short string altered")
	}
}

func TestRunWithDeadlineFailsOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	code := runWithDeadline(ctx, func() int { <-block; return 2 })
	if code != 0 {
		t.Fatalf("timeout code = %d, want 0 (fail-open)", code)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not return at the deadline")
	}
	if got := runWithDeadline(context.Background(), func() int { return 2 }); got != 2 {
		t.Fatalf("fast block decision = %d, want 2", got)
	}
}

func TestLoadMCPConfigReportsParseFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGNT_ERROR_LOG", filepath.Join(dir, "errors.log"))
	if err := os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte("this is {{ not kdl"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cfg := loadMCPConfig(dir, &buf)
	if cfg == nil {
		t.Fatal("nil config; want defaults")
	}
	if !strings.Contains(buf.String(), ".agnt.kdl") {
		t.Fatalf("no visible warning, got %q", buf.String())
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "errors.log")); !strings.Contains(string(data), "mcp") {
		t.Fatalf("not recorded in self log: %q", data)
	}
}

// The version template must stay lazy: an eager getVersionString() dialed the
// daemon on every process start, including the `agnt hook` hot path.
func TestVersionTemplateIsLazy(t *testing.T) {
	if got := rootCmd.VersionTemplate(); got != "{{agntVersion}}" {
		t.Fatalf("VersionTemplate = %q, want the lazy template func", got)
	}
}
