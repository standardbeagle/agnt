package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
)

func TestDigestLineFor(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeIncidentDigest,
		Custom: &proxy.CustomLog{
			Level:   "error",
			Message: "2 new errors",
			Data:    map[string]interface{}{incident.DigestPayloadKey: map[string]int{"n": 2}},
		},
	}
	line, ok := digestLineFor("s1", entry)
	if !ok || line.SessionCode != "s1" || line.Level != "error" || string(line.Payload) != `{"n":2}` {
		t.Fatalf("digest line = %+v ok=%v", line, ok)
	}
	if _, ok := digestLineFor("s1", proxy.LogEntry{Type: proxy.LogTypeError}); ok {
		t.Fatal("non-digest entry rendered as a digest line")
	}
}

func TestHoldOwnedSession_RegistersAndUnregisters(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sockdir.Short(t), "owned.sock")
	_ = daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: sock})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code, stop, err := holdOwnedSession(ctx, sock, project)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, ownedSessionCommand+"-") {
		t.Fatalf("code %q lacks the %s- prefix", code, ownedSessionCommand)
	}

	c := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	got, err := c.SessionGet(code)
	if err != nil {
		t.Fatalf("session not registered: %v", err)
	}
	if p, _ := got["project_path"].(string); p == "" {
		t.Fatalf("registered session has no project path: %v", got)
	}

	// UNREGISTER retires the session on a background goroutine.
	stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := c.SessionGet(code); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s still registered after stop", code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Two agents in one checkout: SESSION ATTACH binds by directory, so it can land
// on the other one. The CLI must refuse rather than pause the wrong session.
func TestSetForwardingForSession_RefusesWhenDirectoryResolvesElsewhere(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sockdir.Short(t), "incidents.sock")
	_ = daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: sock})

	owner := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	if err := owner.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	for _, code := range []string{"agent-a", "agent-b"} {
		if _, err := owner.SessionRegister(code, "-", project, "test", nil); err != nil {
			t.Fatalf("register %s: %v", code, err)
		}
	}

	var ok, refused int
	for _, code := range []string{"agent-a", "agent-b"} {
		c := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
		if err := c.Connect(); err != nil {
			t.Fatal(err)
		}
		err := setForwardingForSession(c, code, true)
		_ = c.Close()
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "refusing to change another session"):
			refused++
		default:
			t.Fatalf("%s: unexpected error: %v", code, err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("ok=%d refused=%d, want exactly one of each", ok, refused)
	}
}
