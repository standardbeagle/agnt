package daemonclient

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
)

// fakeHub is a minimal daemon on a Unix socket that answers PING and
// STATUS/INFO with a fixed version, enough for ResilientClient's version check.
type fakeHub struct {
	ln      net.Listener
	mu      sync.Mutex
	conns   []net.Conn
	stopped bool
	wg      sync.WaitGroup
}

func startFakeHub(t *testing.T, path, version string) *fakeHub {
	t.Helper()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	h := &fakeHub{ln: ln}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			h.mu.Lock()
			if h.stopped {
				// Accepted while stop ran: close it, or its reader never returns.
				h.mu.Unlock()
				_ = c.Close()
				continue
			}
			h.conns = append(h.conns, c)
			h.mu.Unlock()
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				serveFakeHubConn(c, version)
			}()
		}
	}()
	t.Cleanup(h.stop)
	return h
}

func serveFakeHubConn(c net.Conn, version string) {
	r := bufio.NewReader(c)
	w := protocol.NewWriter(c)
	var buf strings.Builder
	for {
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		buf.WriteByte(b)
		frame := buf.String()
		if !strings.HasSuffix(frame, protocol.CommandTerminator) {
			continue
		}
		buf.Reset()
		verb := strings.Fields(strings.TrimSuffix(frame, protocol.CommandTerminator))[0]
		switch verb {
		case "PING":
			_ = w.WritePong()
		case "STATUS", "INFO":
			_ = w.WriteJSON([]byte(`{"version":"` + version + `"}`))
		default:
			_ = w.WriteOK("")
		}
	}
}

// stop closes the listener and every accepted connection, as a daemon that
// exits does.
func (h *fakeHub) stop() {
	h.mu.Lock()
	h.stopped = true
	_ = h.ln.Close()
	for _, c := range h.conns {
		_ = c.Close()
	}
	h.conns = nil
	h.mu.Unlock()
	h.wg.Wait()
}

// After OnVersionMismatch replaces the daemon (the auto-upgrade path) and
// accepts, the connection Connect dialled points at the daemon that was just
// shut down. Connect must hand back a connection to the replacement, so the
// first request succeeds instead of failing with EOF.
func TestResilientClientConnectsToReplacementAfterUpgrade(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	old := startFakeHub(t, sock, "0.0.1")

	upgraded := false
	rc := NewResilientClient(ResilientClientConfig{
		AutoStartConfig: AutoStartConfig{
			SocketPath:   sock,
			DaemonPath:   filepath.Join(t.TempDir(), "never-spawned"),
			StartTimeout: 2 * time.Second,
		},
		ClientVersion: "9.9.9",
		OnVersionMismatch: func(clientVer, daemonVer string) error {
			if upgraded {
				t.Errorf("OnVersionMismatch called again (daemon %s): the replacement was not reached", daemonVer)
				return nil
			}
			upgraded = true
			old.stop()
			startFakeHub(t, sock, clientVer)
			return nil
		},
	})
	t.Cleanup(func() { _ = rc.Close() })

	if err := rc.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !upgraded {
		t.Fatal("OnVersionMismatch was not called")
	}
	if err := rc.Ping(); err != nil {
		t.Fatalf("first request after upgrade failed: %v", err)
	}
}
