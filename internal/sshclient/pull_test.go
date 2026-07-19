package sshclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

type pullEventFixture struct {
	entries chan proxy.LogEntry
	err     error
	mu      sync.Mutex
	filters []protocol.StreamEventFilter
}

func (f *pullEventFixture) StreamEvents(ctx context.Context, filter protocol.StreamEventFilter, handler func(proxy.LogEntry) error) error {
	f.mu.Lock()
	f.filters = append(f.filters, filter)
	f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case entry := <-f.entries:
		if err := handler(entry); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestRemotePullManagerScreenshotEventDownloadsExactBytesAndPrintsPath(t *testing.T) {
	remoteRoot, sc := newSFTPFixture(t)
	remotePath := filepath.Join(remoteRoot, "audit", "mcp-shot.png")
	require.NoError(t, os.MkdirAll(filepath.Dir(remotePath), 0o755))
	wantBytes := []byte("exact-mcp-screenshot-png-bytes")
	require.NoError(t, os.WriteFile(remotePath, wantBytes, 0o644))

	events := &pullEventFixture{entries: make(chan proxy.LogEntry, 1)}
	var noticesMu sync.Mutex
	var notices []string
	mgr := NewRemotePullManager(events, sc, "dev@example", t.TempDir(), func(message string) {
		noticesMu.Lock()
		notices = append(notices, message)
		noticesMu.Unlock()
	})
	mgr.Start(context.Background())
	t.Cleanup(mgr.Stop)
	events.entries <- proxy.LogEntry{Type: proxy.LogTypeScreenshot, Screenshot: &proxy.Screenshot{
		Name: "mcp-shot", FilePath: remotePath, Format: "png",
	}}

	wantPath := filepath.Join(mgr.baseDir, "dev@example", "from-remote", "mcp-shot.png")
	require.Eventually(t, func() bool {
		noticesMu.Lock()
		defer noticesMu.Unlock()
		return len(notices) > 0 && strings.Contains(notices[len(notices)-1], wantPath)
	}, time.Second, 10*time.Millisecond, "successful pull must print the exact activated local path")
	got, err := os.ReadFile(wantPath)
	require.NoError(t, err)
	require.Equal(t, wantBytes, got)

	events.mu.Lock()
	require.ElementsMatch(t, []string{"screenshot", "screenshot_capture", "sketch_capture"}, events.filters[0].Types)
	events.mu.Unlock()
}

func TestRemotePullManagerDownloadRejectsDotDotTraversal(t *testing.T) {
	mgr := NewRemotePullManager(nil, nil, "host", t.TempDir(), nil)
	// Each of these cleans to "..", whose Base is "..": they must be
	// rejected explicitly, not by an incidental EISDIR further down.
	for _, bad := range []string{"..", "../..", "a/../.."} {
		_, err := mgr.download(bad)
		if err == nil {
			t.Fatalf("download(%q) = nil error, want explicit rejection", bad)
		}
		if !strings.Contains(err.Error(), "invalid remote file path") {
			t.Fatalf("download(%q) error = %v, want an invalid-remote-file-path rejection", bad, err)
		}
	}
}

func TestRemotePullManagerDownloadFailureIsLoud(t *testing.T) {
	_, sc := newSFTPFixture(t)
	events := &pullEventFixture{entries: make(chan proxy.LogEntry, 1)}
	notices := make(chan string, 1)
	mgr := NewRemotePullManager(events, sc, "host", t.TempDir(), func(message string) { notices <- message })
	mgr.Start(context.Background())
	defer mgr.Stop()
	events.entries <- proxy.LogEntry{Type: proxy.LogTypeScreenshot, Screenshot: &proxy.Screenshot{FilePath: "/missing/screenshot.png"}}
	select {
	case got := <-notices:
		require.Contains(t, got, "reverse pull failed")
		require.Contains(t, got, "/missing/screenshot.png")
	case <-time.After(time.Second):
		t.Fatal("download failure was silently skipped")
	}
}

func TestRemotePullManagerStreamFailureIsLoud(t *testing.T) {
	events := &pullEventFixture{entries: make(chan proxy.LogEntry), err: errors.New("forwarded socket closed")}
	notices := make(chan string, 1)
	mgr := NewRemotePullManager(events, nil, "host", t.TempDir(), func(message string) { notices <- message })
	mgr.retry = time.Hour
	mgr.Start(context.Background())
	defer mgr.Stop()
	select {
	case got := <-notices:
		require.Contains(t, got, "event stream failed")
		require.Contains(t, got, "forwarded socket closed")
	case <-time.After(time.Second):
		t.Fatal("stream failure was silently skipped")
	}
}

func TestRemotePullManagerReconnectCyclesDoNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	mgr := NewRemotePullManager(nil, nil, "host", t.TempDir(), func(string) {})
	for i := 0; i < 5; i++ {
		events := &pullEventFixture{entries: make(chan proxy.LogEntry)}
		mgr.resume(context.Background(), events, nil)
		mgr.Stop()
	}
}

func TestSafePathComponentContainsHostUnderDropRoot(t *testing.T) {
	require.Equal(t, ".._evil_host", safePathComponent("../evil\\host"))
	require.Equal(t, "remote", safePathComponent("  "))
}
