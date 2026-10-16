package proxy

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotPNG returns a base64 PNG of a solid 8x8 image.
func snapshotPNG(t *testing.T, c color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// newSnapshotProxy is a gate proxy whose baseline store is a temp dir, plus a
// Manager over the same store standing in for the snapshot MCP tool's.
func newSnapshotProxy(t *testing.T) (*ProxyServer, *snapshot.Manager) {
	t.Helper()
	ps := newGateProxy(t)
	ps.snapshotStoragePath = t.TempDir()
	m, err := snapshot.NewManager(ps.snapshotStoragePath, 0.01)
	require.NoError(t, err)
	return ps, m
}

// snapshotRequest dispatches one snapshot_request and returns its reply.
func snapshotRequest(t *testing.T, ps *ProxyServer, trusted bool, data map[string]interface{}) map[string]interface{} {
	t.Helper()
	c, stub := newGateConn(t, ps, trusted)
	data["request_id"] = "snap-1"
	dispatch(c, &wsMessage{Type: "snapshot_request", Data: data})
	require.Eventually(t, func() bool { return stub.count() == 1 }, 3*time.Second, 5*time.Millisecond)
	reply := stub.frames(t)[0]
	assert.Equal(t, "snapshot_response", reply["type"])
	assert.Equal(t, "snap-1", reply["request_id"])
	return reply
}

func snapshotPage(data string) map[string]interface{} {
	return map[string]interface{}{
		"url":             "/home",
		"viewport":        map[string]interface{}{"width": float64(1280), "height": float64(720)},
		"screenshot_data": data,
	}
}

// A browser baseline lands in the store the MCP tool reads, and the reply is
// the saved baseline -- not a promise that something was sent.
func TestSnapshotRequest_BaselineIsSavedWhereTheToolSeesIt(t *testing.T) {
	ps, tool := newSnapshotProxy(t)

	reply := snapshotRequest(t, ps, true, map[string]interface{}{
		"action": "baseline", "name": "home-v1", "page": snapshotPNG(t, color.White),
	})
	// The page key is "page"; a bare string is not a page.
	assert.Contains(t, reply["error"], "screenshot_data is required")

	reply = snapshotRequest(t, ps, true, map[string]interface{}{
		"action": "baseline", "name": "home-v1", "page": snapshotPage(snapshotPNG(t, color.White)),
	})
	require.NotContains(t, reply, "error")
	result := reply["result"].(map[string]interface{})
	assert.Equal(t, "home-v1", result["name"])
	require.Len(t, result["pages"], 1)

	baselines, err := tool.ListBaselines()
	require.NoError(t, err)
	require.Len(t, baselines, 1)
	assert.Equal(t, "home-v1", baselines[0].Name)
	assert.Equal(t, "/home", baselines[0].Pages[0].URL)
	assert.Equal(t, 1280, baselines[0].Pages[0].Viewport.Width)

	// And the tool compares against it.
	cmp, err := tool.CompareToBaseline("home-v1", []snapshot.PageCapture{{URL: "/home", ScreenshotData: snapshotPNG(t, color.White)}}, 0)
	require.NoError(t, err)
	assert.False(t, cmp.Summary.HasRegressions)
}

// A browser comparison runs against a baseline the tool created and replies
// with the real CompareResult.
func TestSnapshotRequest_CompareRepliesWithTheResult(t *testing.T) {
	ps, tool := newSnapshotProxy(t)
	_, err := tool.CreateBaseline("tool-made", []snapshot.PageCapture{{URL: "/home", ScreenshotData: snapshotPNG(t, color.White)}})
	require.NoError(t, err)

	same := snapshotRequest(t, ps, true, map[string]interface{}{
		"action": "compare", "name": "tool-made", "page": snapshotPage(snapshotPNG(t, color.White)),
	})
	require.NotContains(t, same, "error")
	summary := same["result"].(map[string]interface{})["summary"].(map[string]interface{})
	assert.Equal(t, false, summary["has_regressions"])
	assert.Equal(t, float64(1), summary["pages_unchanged"])

	changed := snapshotRequest(t, ps, true, map[string]interface{}{
		"action": "compare", "name": "tool-made", "page": snapshotPage(snapshotPNG(t, color.Black)),
	})
	require.NotContains(t, changed, "error")
	result := changed["result"].(map[string]interface{})
	assert.Equal(t, true, result["summary"].(map[string]interface{})["has_regressions"])
	page := result["pages"].([]interface{})[0].(map[string]interface{})
	assert.Greater(t, page["diff_percentage"], float64(50))
}

func TestSnapshotRequest_Errors(t *testing.T) {
	ps, tool := newSnapshotProxy(t)
	shot := snapshotPNG(t, color.White)

	cases := []struct {
		name string
		data map[string]interface{}
		want string
	}{
		{"unknown baseline", map[string]interface{}{"action": "compare", "name": "nope", "page": snapshotPage(shot)}, `failed to compare to baseline "nope"`},
		{"missing name", map[string]interface{}{"action": "baseline", "page": snapshotPage(shot)}, "baseline name is required"},
		{"unknown action", map[string]interface{}{"action": "delete", "name": "x", "page": snapshotPage(shot)}, `unknown snapshot action "delete"`},
		{"not a png", map[string]interface{}{"action": "baseline", "name": "x", "page": snapshotPage(base64.StdEncoding.EncodeToString([]byte("GIF89a-not-a-png")))}, "not a base64-encoded PNG"},
		{"bad base64", map[string]interface{}{"action": "baseline", "name": "x", "page": snapshotPage(shot[:16] + "!!!!")}, `failed to create baseline "x"`},
		// The page may not name a file for the daemon to read.
		{"path instead of data", map[string]interface{}{"action": "baseline", "name": "x", "page": map[string]interface{}{"url": "/", "screenshot_path": os.Args[0]}}, "screenshot_data is required"},
		{"escaping name", map[string]interface{}{"action": "baseline", "name": "..", "page": snapshotPage(shot)}, "failed to create baseline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := snapshotRequest(t, ps, true, tc.data)
			assert.Contains(t, reply["error"], tc.want)
			assert.NotContains(t, reply, "result")
		})
	}
	baselines, err := tool.ListBaselines()
	require.NoError(t, err)
	assert.Empty(t, baselines, "no failed request may leave a baseline behind")
}

// A capture over the cap is refused whole, with the sizes in the message.
func TestSnapshotRequest_OversizeRefused(t *testing.T) {
	ps, tool := newSnapshotProxy(t)
	shot := snapshotPNG(t, color.White)
	big := shot + strings.Repeat("A", maxSnapshotScreenshotBase64-len(shot)+4)

	reply := snapshotRequest(t, ps, true, map[string]interface{}{
		"action": "baseline", "name": "big", "page": snapshotPage(big),
	})
	assert.Contains(t, reply["error"], "over the "+strconv.Itoa(maxSnapshotScreenshotBase64)+" byte limit")
	baselines, err := tool.ListBaselines()
	require.NoError(t, err)
	assert.Empty(t, baselines)
}

// An untrusted connection (a tunnel visitor) may not write to the operator's
// baseline store; it is told why.
func TestSnapshotRequest_UntrustedRefused(t *testing.T) {
	ps, tool := newSnapshotProxy(t)

	reply := snapshotRequest(t, ps, false, map[string]interface{}{
		"action": "baseline", "name": "visitor", "page": snapshotPage(snapshotPNG(t, color.White)),
	})
	assert.Contains(t, reply["error"], privilegedRefusalMessage)
	assert.NotContains(t, reply, "result")
	assert.Contains(t, diagnosticEvents(ps), "ws_privileged_frame_refused")

	baselines, err := tool.ListBaselines()
	require.NoError(t, err)
	assert.Empty(t, baselines)
}

// snapshot-helper.js refuses an oversize capture before sending it, against
// the same cap the handler enforces. Behaviour is proven in the JS tier
// (jstest/snapshot-helper.test.js); this keeps the number in step.
func TestSnapshotHelper_SizeCapMatchesGo(t *testing.T) {
	src, err := os.ReadFile("scripts/snapshot-helper.js")
	require.NoError(t, err)
	m := regexp.MustCompile(`var MAX_SCREENSHOT_BASE64 = (\d+) \* 1024 \* 1024;`).FindSubmatch(src)
	require.NotNil(t, m, "snapshot-helper.js must declare MAX_SCREENSHOT_BASE64")
	mib, err := strconv.Atoi(string(m[1]))
	require.NoError(t, err)
	assert.Equal(t, maxSnapshotScreenshotBase64, mib<<20)
}
