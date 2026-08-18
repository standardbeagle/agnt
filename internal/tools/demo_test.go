package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-sdk/mcp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFixtureEngine lays out a minimal in-repo demo engine tree under root:
//
//	<root>/docs-site/screenshots/engine/demo.mjs   (stub: echoes argv, then idles)
//	<root>/docs-site/screenshots/demos/<name>/demo.json
//
// The stub demo.mjs is deliberately dependency-free (no Chrome/ffmpeg/edge-tts)
// so the managed-process path can be exercised in the default suite. It prints
// a marker line immediately (so `proc output` has something to observe) then
// idles forever (so `proc status` reports running until the test stops it).
func writeFixtureEngine(t *testing.T, root, demoName, demoJSON string) (engineEntry, screenshots string) {
	t.Helper()
	screenshots = filepath.Join(root, "docs-site", "screenshots")
	engineDir := filepath.Join(screenshots, "engine")
	require.NoError(t, os.MkdirAll(engineDir, 0o755))
	engineEntry = filepath.Join(engineDir, "demo.mjs")
	stub := "console.log('STUB DEMO ' + process.argv.slice(2).join(' '));\n" +
		"setInterval(() => {}, 1000);\n"
	require.NoError(t, os.WriteFile(engineEntry, []byte(stub), 0o644))

	if demoName != "" {
		demoDir := filepath.Join(screenshots, "demos", demoName)
		require.NoError(t, os.MkdirAll(demoDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(demoDir, "demo.json"), []byte(demoJSON), 0o644))
	}
	return engineEntry, screenshots
}

const fixtureDemoJSON = `{
  "name": "fixture",
  "segments": [
    {"id": "card-intro", "type": "card"},
    {"id": "attempt-1", "type": "cli"},
    {"id": "fix", "type": "browser"}
  ],
  "narration": {
    "voice": "en-US-TestVoice",
    "segments": [
      {"id": "n1", "at": "fix+0.5", "text": "hello"},
      {"id": "n2", "at": "fix+3", "text": "world"}
    ]
  }
}`

func TestResolveDemoEngine_MissingIsLoud(t *testing.T) {
	root := t.TempDir() // no engine tree
	_, err := resolveDemoEngine(root)
	require.Error(t, err, "missing engine dir must be a loud error, never a silent no-op")
	msg := err.Error()
	assert.Contains(t, msg, "demo.mjs", "error must name the required engine entry")
	assert.Contains(t, msg, "repo-checkout", "error must say this is a repo-checkout capability, not a binary feature")
}

func TestResolveDemoEngine_Present(t *testing.T) {
	root := t.TempDir()
	engineEntry, screenshots := writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)

	eng, err := resolveDemoEngine(root)
	require.NoError(t, err)
	assert.Equal(t, engineEntry, eng.Entry)
	assert.Equal(t, screenshots, eng.Screenshots)
	assert.Equal(t, filepath.Join(screenshots, "demos"), eng.DemosDir)
}

func TestListDemos_SummarizesSegmentsAndNarration(t *testing.T) {
	root := t.TempDir()
	writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)
	eng, err := resolveDemoEngine(root)
	require.NoError(t, err)

	demos, err := listDemos(eng.DemosDir)
	require.NoError(t, err)
	require.Len(t, demos, 1)
	d := demos[0]
	assert.Equal(t, "fixture", d.Name)
	assert.Equal(t, 3, d.SegmentCount)
	assert.Equal(t, 3, len(d.Segments))
	assert.True(t, d.HasNarration)
	assert.Equal(t, "en-US-TestVoice", d.NarrationVoice)
	assert.Equal(t, 2, d.NarrationSegments)
}

func TestListDemos_NarrationOptional(t *testing.T) {
	root := t.TempDir()
	writeFixtureEngine(t, root, "silent", `{"name":"silent","segments":[{"id":"a","type":"cli"}]}`)
	eng, err := resolveDemoEngine(root)
	require.NoError(t, err)
	demos, err := listDemos(eng.DemosDir)
	require.NoError(t, err)
	require.Len(t, demos, 1)
	assert.False(t, demos[0].HasNarration)
	assert.Equal(t, 0, demos[0].NarrationSegments)
}

func TestBuildDemoEngineArgs(t *testing.T) {
	entry := "/x/engine/demo.mjs"
	assert.Equal(t, []string{entry, "demos/foo"}, buildDemoEngineArgs(entry, "foo", "", false))
	assert.Equal(t, []string{entry, "demos/foo", "--only=seg1"}, buildDemoEngineArgs(entry, "foo", "seg1", false))
	assert.Equal(t, []string{entry, "demos/foo", "--assemble-only"}, buildDemoEngineArgs(entry, "foo", "", true))
}

func TestDemoHandler_MissingEngineIsError(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: "/tmp/none.sock"}, "")
	h := dt.makeDemoHandler()
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, DemoInput{
		Action: "list",
		Path:   t.TempDir(), // no engine tree
	})
	require.NoError(t, err, "tool errors travel as CallToolResult, not Go errors")
	require.NotNil(t, res)
	assert.True(t, res.IsError, "missing engine dir must yield IsError")
}

func TestDemoHandler_InspectPublishNotYetAvailable(t *testing.T) {
	root := t.TempDir()
	writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: "/tmp/none.sock"}, "")
	h := dt.makeDemoHandler()
	for _, action := range []string{"inspect", "publish"} {
		res, _, err := h(context.Background(), &mcp.CallToolRequest{}, DemoInput{
			Action: action,
			Name:   "fixture",
			Path:   root,
		})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.IsError, "%s must be a loud not-yet-available error, not a silent no-op", action)
	}
}

func TestDemoHandler_ListEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: "/tmp/none.sock"}, "")
	h := dt.makeDemoHandler()
	res, out, err := h(context.Background(), &mcp.CallToolRequest{}, DemoInput{Action: "list", Path: root})
	require.NoError(t, err)
	require.False(t, res != nil && res.IsError)
	require.Equal(t, 1, out.Count)
	require.Len(t, out.Demos, 1)
	assert.Equal(t, "fixture", out.Demos[0].Name)
}

// shortDemoSock returns a socket path short enough for the unix socket limit.
func shortDemoSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "d")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// TestDemoHandler_RecordManagedProcess exercises the full record path against a
// live NewForTest daemon and a stub engine: the tool returns a process id
// immediately, and proc status/output/stop observe the managed recording.
// Node-guarded: skips loudly when node is absent (the stub engine is a .mjs).
func TestDemoHandler_RecordManagedProcess(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping stub-engine managed-process integration test")
	}
	root := t.TempDir()
	writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)

	sock := shortDemoSock(t)
	daemon.NewForTest(t, daemon.DaemonConfig{
		SocketPath:   sock,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	// version "" skips the client/daemon version check (no auto-upgrade path).
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: sock}, "")
	h := dt.makeDemoHandler()

	res, out, err := h(context.Background(), &mcp.CallToolRequest{}, DemoInput{
		Action: "record",
		Name:   "fixture",
		Path:   root,
	})
	require.NoError(t, err)
	require.False(t, res != nil && res.IsError, "record should not error against a valid engine")
	require.NotEmpty(t, out.ProcessID, "record must return a process id immediately")

	procH := dt.makeProcHandler()
	// Output is observable (stub prints a marker line once node boots).
	require.Eventually(t, func() bool {
		_, pout, perr := procH(context.Background(), &mcp.CallToolRequest{}, ProcInput{
			Action:    "output",
			ProcessID: out.ProcessID,
		})
		return perr == nil && pout.Output != ""
	}, 8*time.Second, 150*time.Millisecond, "stub record output should become observable via proc")

	// Status is observable.
	_, sout, serr := procH(context.Background(), &mcp.CallToolRequest{}, ProcInput{
		Action:    "status",
		ProcessID: out.ProcessID,
	})
	require.NoError(t, serr)
	assert.Equal(t, out.ProcessID, sout.ProcessID)

	// Stop is observable and succeeds.
	_, stopOut, stopErr := procH(context.Background(), &mcp.CallToolRequest{}, ProcInput{
		Action:    "stop",
		ProcessID: out.ProcessID,
	})
	require.NoError(t, stopErr)
	assert.True(t, stopOut.Success, "proc stop should succeed for the managed recording")
}
