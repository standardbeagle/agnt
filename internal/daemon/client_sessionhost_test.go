//go:build unix

package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sessionhost"
	"github.com/stretchr/testify/require"
)

// TestClientSessionHost_FullCycle exercises the daemonclient.Client SESSION-HOST
// wrapper methods (added for `agnt attach` / `agnt session hosts|kill`) end
// to end: create a real PTY session, attach and observe the replay+live
// frame stream, send primary stdin, detach without killing, re-attach, then
// kill and confirm the still-open attach observes the exit frame. This is
// the client-side counterpart to hub_sessionhost_test.go's server-side
// coverage — it pins the *daemonclient.Client wrapper's wire format, not the hub
// dispatch logic.
func TestClientSessionHost_FullCycle(t *testing.T) {
	// No t.Parallel(): spawns a real `sh` PTY child reaped via killpg — see
	// AGENTS.md prohibition on parallel tests that start real OS processes.
	_, sockPath := newSessionHostTestDaemon(t)

	client := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()

	created, err := client.SessionHostCreate(protocol.SessionHostCreateConfig{
		ProjectPath: t.TempDir(),
		Command:     "sh",
		Args:        []string{"-c", "echo preexisting; cat"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.SessionID)
	require.Greater(t, created.SessionPGID, 0)

	time.Sleep(200 * time.Millisecond) // let "preexisting" land in scrollback

	// --- First attach: observe attach_id/primary + replay + live echo ---
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	var (
		mu         sync.Mutex
		attachID   string
		isPrimary  bool
		replayed   strings.Builder
		gotAttach  = make(chan struct{})
		attachOnce sync.Once
	)

	go func() {
		_ = client.SessionHostAttach(ctx1, created.SessionID,
			func(id string, primary bool) {
				mu.Lock()
				attachID, isPrimary = id, primary
				mu.Unlock()
				attachOnce.Do(func() { close(gotAttach) })
			},
			func(f sessionhost.Frame) error {
				if f.Type == "stdout" {
					var b64 string
					_ = json.Unmarshal(f.Data, &b64)
					raw, _ := base64.StdEncoding.DecodeString(b64)
					mu.Lock()
					replayed.Write(raw)
					mu.Unlock()
				}
				return nil
			},
		)
	}()

	select {
	case <-gotAttach:
	case <-time.After(3 * time.Second):
		t.Fatal("attach did not deliver an 'attached' frame")
	}
	mu.Lock()
	require.NotEmpty(t, attachID)
	require.True(t, isPrimary)
	mu.Unlock()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(replayed.String(), "preexisting")
	}, 3*time.Second, 20*time.Millisecond, "replay did not contain pre-existing scrollback")

	// Primary stdin: write "hello\n" to the cat child, expect it echoed
	// back as a live stdout frame.
	require.NoError(t, client.SessionHostStdin(created.SessionID, attachID, []byte("hello\n")))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(replayed.String(), "hello")
	}, 3*time.Second, 20*time.Millisecond, "stdin write was not echoed back")

	// Resize must not error even though nothing asserts on the PTY size here.
	require.NoError(t, client.SessionHostResize(created.SessionID, 100, 40))

	// Detach: ends this attach's stream, does NOT kill the PTY child.
	require.NoError(t, client.SessionHostDetach(created.SessionID, attachID))

	// --- Second attach (re-attach after detach): session must still be alive ---
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	var (
		attachID2  string
		isPrimary2 bool
		gotAttach2 = make(chan struct{})
		once2      sync.Once
		gotExit    = make(chan int, 1)
	)
	go func() {
		_ = client.SessionHostAttach(ctx2, created.SessionID,
			func(id string, primary bool) {
				attachID2, isPrimary2 = id, primary
				once2.Do(func() { close(gotAttach2) })
			},
			func(f sessionhost.Frame) error {
				if f.Type == "exit" {
					var ed sessionhost.ExitData
					_ = json.Unmarshal(f.Data, &ed)
					select {
					case gotExit <- ed.Code:
					default:
					}
				}
				return nil
			},
		)
	}()

	select {
	case <-gotAttach2:
	case <-time.After(3 * time.Second):
		t.Fatal("re-attach did not deliver an 'attached' frame")
	}
	require.NotEmpty(t, attachID2)
	require.True(t, isPrimary2, "the primary slot should be reclaimable after the prior primary detached")

	// List should show one detachable session with attached_count == 1.
	list, err := client.SessionHostList(protocol.DirectoryFilter{Global: true})
	require.NoError(t, err)
	sessions, _ := list["sessions"].([]interface{})
	require.Len(t, sessions, 1)
	sm := sessions[0].(map[string]interface{})
	require.Equal(t, float64(1), sm["attached_count"])

	// --- Kill: the still-attached second connection must observe "exit" ---
	require.NoError(t, client.SessionHostKill(created.SessionID))

	select {
	case code := <-gotExit:
		require.Equal(t, -1, code) // KILL reaps the pgid via signal, not a clean stdin-close exit
	case <-time.After(3 * time.Second):
		t.Fatal("attached client did not observe an exit frame after SESSION-HOST KILL")
	}

	// Session-host session must be gone from LIST after kill.
	listAfter, err := client.SessionHostList(protocol.DirectoryFilter{Global: true})
	require.NoError(t, err)
	sessionsAfter, _ := listAfter["sessions"].([]interface{})
	require.Empty(t, sessionsAfter)
}
