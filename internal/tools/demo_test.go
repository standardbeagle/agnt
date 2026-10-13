package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	assert.Equal(t, []string{entry, "demos/foo"}, buildDemoEngineArgs(entry, "foo", "record", ""))
	assert.Equal(t, []string{entry, "demos/foo", "--only=seg1,seg2"}, buildDemoEngineArgs(entry, "foo", "record", "seg1,seg2"))
	assert.Equal(t, []string{entry, "demos/foo", "--assemble-only"}, buildDemoEngineArgs(entry, "foo", "assemble", "ignored"))
	assert.Equal(t, []string{entry, "demos/foo", "--inspect"}, buildDemoEngineArgs(entry, "foo", "inspect", ""))
	assert.Equal(t, []string{entry, "demos/foo", "--inspect=seg1"}, buildDemoEngineArgs(entry, "foo", "inspect", "seg1"))
	assert.Equal(t, []string{entry, "demos/foo", "--publish"}, buildDemoEngineArgs(entry, "foo", "publish", "ignored"))
}

func TestValidDemoName(t *testing.T) {
	for _, ok := range []string{"vhs-spiral", "ci_fixture", "demo.v2", "A1"} {
		assert.True(t, validDemoName(ok), ok)
	}
	for _, bad := range []string{"", "..", "../x", "a/b", `a\b`, ".hidden", "a..b", "x y", "--publish"} {
		assert.False(t, validDemoName(bad), bad)
	}
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

// Requests the engine could not honour are refused before any process starts
// (the socket here is unreachable, so reaching the daemon would be a different
// error): a name that is not a plain directory name, and an inspect scoped to
// more than one segment.
func TestDemoHandler_RefusesBadEngineRequests(t *testing.T) {
	root := t.TempDir()
	writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: "/tmp/none.sock"}, "")
	h := dt.makeDemoHandler()
	cases := []struct {
		in   DemoInput
		want string
	}{
		{DemoInput{Action: "publish", Name: "../fixture"}, "invalid name"},
		{DemoInput{Action: "inspect", Name: "--publish"}, "invalid name"},
		{DemoInput{Action: "inspect", Name: "fixture", Only: "a,b"}, "single segment id"},
		{DemoInput{Action: "publish", Name: "absent"}, "no demo named"},
	}
	for _, c := range cases {
		c.in.Path = root
		res, _, err := h(context.Background(), &mcp.CallToolRequest{}, c.in)
		require.NoError(t, err)
		require.NotNil(t, res, "%+v", c.in)
		require.True(t, res.IsError, "%+v must be refused", c.in)
		assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, c.want)
	}
}

// TestDemoHandler_InspectAndPublishReachTheEngine runs both actions against a
// live NewForTest daemon and the stub engine, and asserts the engine was
// invoked with the flag each action maps to — the actions used to return
// "not yet available" without starting anything.
func TestDemoHandler_InspectAndPublishReachTheEngine(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping stub-engine managed-process integration test")
	}
	root := t.TempDir()
	writeFixtureEngine(t, root, "fixture", fixtureDemoJSON)
	sock := shortDemoSock(t)
	daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: sock, MaxClients: 10, WriteTimeout: 5 * time.Second})
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: sock}, "")
	h := dt.makeDemoHandler()
	procH := dt.makeProcHandler()

	cases := []struct {
		in       DemoInput
		wantID   string
		wantArgv string
		wantHint string
	}{
		{DemoInput{Action: "inspect", Name: "fixture", Only: "fix"}, "demo-inspect-fixture", "STUB DEMO demos/fixture --inspect=fix", filepath.Join("out", "inspect")},
		{DemoInput{Action: "publish", Name: "fixture"}, "demo-publish-fixture", "STUB DEMO demos/fixture --publish", filepath.Join("docs-site", "static", "video")},
	}
	for _, c := range cases {
		c.in.Path = root
		res, out, err := h(context.Background(), &mcp.CallToolRequest{}, c.in)
		require.NoError(t, err)
		require.False(t, res != nil && res.IsError, "%s should start against a valid engine", c.in.Action)
		assert.Contains(t, out.ProcessID, c.wantID)
		assert.Contains(t, out.Message, c.wantHint, "the message must say where the result lands")

		var got string
		require.Eventually(t, func() bool {
			_, pout, perr := procH(context.Background(), &mcp.CallToolRequest{}, ProcInput{Action: "output", ProcessID: out.ProcessID})
			got = pout.Output
			return perr == nil && strings.Contains(got, c.wantArgv)
		}, 8*time.Second, 150*time.Millisecond, "engine argv for %s: want %q in output %q", c.in.Action, c.wantArgv, got)

		_, stopOut, stopErr := procH(context.Background(), &mcp.CallToolRequest{}, ProcInput{Action: "stop", ProcessID: out.ProcessID})
		require.NoError(t, stopErr)
		assert.True(t, stopOut.Success)
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

// TestListDemos_NameIsTheDirectoryName: `name` in the listing is what every
// other action takes. A demo whose demo.json names it differently used to be
// listed under the spec name, which record/assemble/inspect/publish then could
// not find.
func TestListDemos_NameIsTheDirectoryName(t *testing.T) {
	root := t.TempDir()
	writeFixtureEngine(t, root, "vhs-spiral", `{"name":"spiral","segments":[{"id":"a","type":"cli"}]}`)
	writeFixtureEngine(t, root, "same", `{"name":"same","segments":[]}`)
	eng, err := resolveDemoEngine(root)
	require.NoError(t, err)

	demos, err := listDemos(eng.DemosDir)
	require.NoError(t, err)
	require.Len(t, demos, 2)
	byName := map[string]DemoSummary{}
	for _, d := range demos {
		byName[d.Name] = d
	}

	spiral, ok := byName["vhs-spiral"]
	require.True(t, ok, "the demo must be listed under its directory name, got %+v", demos)
	assert.Equal(t, "spiral", spiral.DisplayName, "demo.json's name is reported separately")
	_, listedBySpecName := byName["spiral"]
	assert.False(t, listedBySpecName, "the spec name must not be handed back as `name`")
	// Every listed name resolves to a real demo directory — the property the
	// other actions depend on.
	for name := range byName {
		_, statErr := os.Stat(filepath.Join(eng.DemosDir, name, "demo.json"))
		assert.NoError(t, statErr, "listed name %q is not a demo directory", name)
	}
	// Near miss: identical names carry no redundant display name.
	assert.Equal(t, "", byName["same"].DisplayName)
}

func TestValidateDemoOnly(t *testing.T) {
	accepted := []struct{ action, only string }{
		{"record", ""}, {"assemble", ""}, {"publish", ""}, {"inspect", ""}, {"list", ""},
		{"record", "card-intro"},
		{"record", "card-intro,fix,attempt_1,seg.2"},
		{"inspect", "fix"},
	}
	for _, c := range accepted {
		assert.NoError(t, validateDemoOnly(c.action, c.only), "%s only=%q must be accepted", c.action, c.only)
	}

	refused := []struct{ action, only, want string }{
		// No consumer: refused rather than accepted and ignored.
		{"assemble", "fix", "not supported for this action"},
		{"publish", "fix", "not supported for this action"},
		{"list", "fix", "not supported for this action"},
		// inspect takes exactly one id.
		{"inspect", "a,b", "single segment id"},
		// Shapes that are not segment ids never reach the engine's argv.
		{"record", "fix,", "comma-separated list of segment ids"},
		{"record", ",fix", "comma-separated list of segment ids"},
		{"record", "fix --publish", "comma-separated list of segment ids"},
		{"record", "-rf", "comma-separated list of segment ids"},
		{"record", "a;rm -rf x", "comma-separated list of segment ids"},
		{"record", "../x", "comma-separated list of segment ids"},
		{"record", "a b", "comma-separated list of segment ids"},
		{"inspect", "--assemble-only", "must be a segment id"},
		{"inspect", "a&b", "must be a segment id"},
	}
	for _, c := range refused {
		err := validateDemoOnly(c.action, c.only)
		if assert.Error(t, err, "%s only=%q must be refused", c.action, c.only) {
			assert.Contains(t, err.Error(), c.want, "%s only=%q", c.action, c.only)
		}
	}
}
