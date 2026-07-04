//go:build unix

package daemon

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sessionhost"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/require"
)

// rawConn is a minimal test-only client that speaks the wire protocol
// directly, bypassing the daemon.Client convenience wrapper (which has no
// generic streaming-read primitive). It lets a test send a command, then
// read chunk-by-chunk — needed to capture the attach_id from SESSION-HOST
// ATTACH's first frame before issuing a DETACH from a second connection.
type rawConn struct {
	conn   net.Conn
	writer *hubproto.Writer
	parser *hubproto.Parser
}

func dialRaw(t *testing.T, sockPath string) *rawConn {
	t.Helper()
	c, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	rc := &rawConn{conn: c, writer: hubproto.NewWriter(c), parser: hubproto.NewParser(c)}
	t.Cleanup(func() { _ = c.Close() })
	return rc
}

func (r *rawConn) send(verb string, args []string, payload interface{}) {
	var data []byte
	if payload != nil {
		data, _ = json.Marshal(payload)
	}
	err := r.writer.WriteCommand(verb, args, data)
	if err != nil {
		panic(err)
	}
}

func (r *rawConn) readResponse(t *testing.T) *hubproto.Response {
	t.Helper()
	resp, err := r.parser.ParseResponse()
	require.NoError(t, err)
	return resp
}

func newSessionHostTestDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")
	d := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   64,
		WriteTimeout: 5 * time.Second,
	})
	return d, sockPath
}

func TestSessionHostCreate_ReturnsSessionIDAndPGID(t *testing.T) {
	t.Parallel()
	_, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	var result protocol.SessionHostCreateResult
	err := c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		ProjectPath: t.TempDir(),
		Command:     "sh",
		Args:        []string{"-c", "cat"},
		Cols:        80,
		Rows:        24,
	}).JSONInto(&result)
	require.NoError(t, err)
	require.NotEmpty(t, result.SessionID)
	require.Greater(t, result.SessionPGID, 0)

	// Explicit kill so the test doesn't leak the PTY child.
	require.NoError(t, c.Request("SESSION-HOST", "KILL", result.SessionID).OK())
}

func TestSessionHostList_ProjectScoped(t *testing.T) {
	t.Parallel()
	_, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	proj := t.TempDir()
	var created protocol.SessionHostCreateResult
	require.NoError(t, c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		ProjectPath: proj,
		Command:     "sh",
		Args:        []string{"-c", "cat"},
	}).JSONInto(&created))
	defer c.Request("SESSION-HOST", "KILL", created.SessionID).OK()

	result, err := c.Request("SESSION-HOST", "LIST").WithJSON(protocol.DirectoryFilter{Directory: proj}).JSON()
	require.NoError(t, err)
	require.EqualValues(t, 1, result["count"])

	otherResult, err := c.Request("SESSION-HOST", "LIST").WithJSON(protocol.DirectoryFilter{Directory: t.TempDir()}).JSON()
	require.NoError(t, err)
	require.EqualValues(t, 0, otherResult["count"])
}

func TestSessionHostKill_ReapsAndRemovesFromBothRegistries(t *testing.T) {
	t.Parallel()
	d, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	var created protocol.SessionHostCreateResult
	require.NoError(t, c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		ProjectPath: t.TempDir(),
		Command:     "sh",
		Args:        []string{"-c", "cat"},
	}).JSONInto(&created))

	require.NoError(t, c.Request("SESSION-HOST", "KILL", created.SessionID).OK())

	_, ok := d.sessionHosts.Get(created.SessionID)
	require.False(t, ok, "session-host registry should no longer have the killed session")
	_, ok = d.sessionRegistry.Get(created.SessionID)
	require.False(t, ok, "shared session registry should no longer have the killed session")
}

func TestSessionHostAttach_ReplaysThenLive_DetachDoesNotKillPTY(t *testing.T) {
	t.Parallel()
	d, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	var created protocol.SessionHostCreateResult
	require.NoError(t, c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		ProjectPath: t.TempDir(),
		Command:     "sh",
		Args:        []string{"-c", "printf preexisting; sleep 5"},
	}).JSONInto(&created))
	defer func() { _ = c.Request("SESSION-HOST", "KILL", created.SessionID).OK() }()

	time.Sleep(200 * time.Millisecond) // let "preexisting" land in scrollback first

	attach := dialRaw(t, sockPath)
	attach.send("SESSION-HOST", []string{"ATTACH", created.SessionID}, nil)

	// First response: OK("streaming").
	resp := attach.readResponse(t)
	require.Equal(t, hubproto.ResponseOK, resp.Type)

	// Next chunk: "attached" frame carrying attach_id.
	resp = attach.readResponse(t)
	require.Equal(t, hubproto.ResponseChunk, resp.Type)
	var attachedFrame sessionhost.Frame
	require.NoError(t, json.Unmarshal(resp.Data, &attachedFrame))
	require.Equal(t, "attached", attachedFrame.Type)
	var attachedData struct {
		AttachID  string `json:"attach_id"`
		IsPrimary bool   `json:"is_primary"`
	}
	require.NoError(t, json.Unmarshal(attachedFrame.Data, &attachedData))
	require.NotEmpty(t, attachedData.AttachID)
	require.True(t, attachedData.IsPrimary)

	// Next chunk: replay-marker.
	resp = attach.readResponse(t)
	var marker sessionhost.Frame
	require.NoError(t, json.Unmarshal(resp.Data, &marker))
	require.Equal(t, "replay-marker", marker.Type)

	// Next chunk(s): the pre-existing scrollback replayed as stdout.
	var replayed strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(replayed.String(), "preexisting") && time.Now().Before(deadline) {
		resp = attach.readResponse(t)
		var f sessionhost.Frame
		require.NoError(t, json.Unmarshal(resp.Data, &f))
		if f.Type == "stdout" {
			var b64 string
			require.NoError(t, json.Unmarshal(f.Data, &b64))
			raw, err := base64.StdEncoding.DecodeString(b64)
			require.NoError(t, err)
			replayed.Write(raw)
		}
	}
	require.Contains(t, replayed.String(), "preexisting")

	// Detach from a second connection using the attach_id we captured.
	ctrl := NewConn(sockPath)
	defer ctrl.Close()
	require.NoError(t, ctrl.Request("SESSION-HOST", "DETACH", created.SessionID).
		WithJSON(map[string]string{"attach_id": attachedData.AttachID}).OK())

	// The attach stream should end (END response) shortly after detach.
	done := make(chan struct{})
	go func() {
		for {
			resp, err := attach.parser.ParseResponse()
			if err != nil || resp.Type == hubproto.ResponseEnd {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("attach stream did not end after detach")
	}

	// Detach must NOT have killed the PTY child — session-host session is
	// still active in the registry (spec §1.3 invariant 4).
	s, ok := d.sessionHosts.Get(created.SessionID)
	require.True(t, ok)
	require.Equal(t, sessionhost.StatusRunning, s.Status())
}

func TestSessionHostStdin_RejectedForNonPrimaryAttach(t *testing.T) {
	t.Parallel()
	_, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	var created protocol.SessionHostCreateResult
	require.NoError(t, c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		ProjectPath: t.TempDir(),
		Command:     "sh",
		Args:        []string{"-c", "cat"},
	}).JSONInto(&created))
	defer func() { _ = c.Request("SESSION-HOST", "KILL", created.SessionID).OK() }()

	err := c.Request("SESSION-HOST", "STDIN", created.SessionID).WithJSON(map[string]string{
		"attach_id": "not-a-real-attach-id",
		"data":      base64.StdEncoding.EncodeToString([]byte("hello\n")),
	}).OK()
	require.Error(t, err, "stdin from an unknown/non-primary attach id must be rejected")
}

// TestSessionHostAttachDetachChurn_Race exercises concurrent attach/detach
// cycles against the daemon over real socket connections under -race,
// matching this task's acceptance criterion.
// Goroutine-leak coverage for the attach/detach fan-out itself lives in
// internal/sessionhost's TestAttachDetachChurn_Race, which runs goleak in
// isolation (a single package with no other concurrently-executing tests).
// This daemon-level test intentionally does not also assert goleak.VerifyNone:
// this package runs hundreds of t.Parallel() tests in the same process
// (autostart, duplicate scanner, URL tracker, ...), and goroutines those
// unrelated tests spawn during this test's window are indistinguishable from
// a real leak to a process-wide goroutine snapshot, making the assertion
// flaky for reasons having nothing to do with session-host correctness.
func TestSessionHostAttachDetachChurn_Race(t *testing.T) {
	t.Parallel()
	_, sockPath := newSessionHostTestDaemon(t)

	c := NewConn(sockPath)
	defer c.Close()

	var created protocol.SessionHostCreateResult
	require.NoError(t, c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		ProjectPath: t.TempDir(),
		Command:     "sh",
		Args:        []string{"-c", "while true; do echo tick; sleep 0.01; done"},
	}).JSONInto(&created))
	defer func() { _ = c.Request("SESSION-HOST", "KILL", created.SessionID).OK() }()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := dialRaw(t, sockPath)
			defer conn.conn.Close()
			for j := 0; j < 5; j++ {
				conn.send("SESSION-HOST", []string{"ATTACH", created.SessionID}, nil)
				_ = conn.readResponse(t) // OK
				resp := conn.readResponse(t)
				var f sessionhost.Frame
				if json.Unmarshal(resp.Data, &f) != nil || f.Type != "attached" {
					continue
				}
				var ad struct {
					AttachID string `json:"attach_id"`
				}
				_ = json.Unmarshal(f.Data, &ad)

				ctrl := NewConn(sockPath)
				if err := ctrl.Request("SESSION-HOST", "DETACH", created.SessionID).
					WithJSON(map[string]string{"attach_id": ad.AttachID}).OK(); err != nil {
					t.Errorf("detach failed: %v", err)
				}
				ctrl.Close()

				// Drain until the stream ends.
				for {
					resp, err := conn.parser.ParseResponse()
					if err != nil || resp.Type == hubproto.ResponseEnd {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
}
