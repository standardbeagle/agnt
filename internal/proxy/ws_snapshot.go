package proxy

import (
	"bytes"
	"encoding/base64"
	"fmt"

	"github.com/standardbeagle/agnt/internal/snapshot"
)

// maxSnapshotScreenshotBase64 caps the base64 screenshot one snapshot_request
// may carry. It sits below wsMaxMessageBytes (16 MiB) so the frame around it
// still fits: a capture over the WebSocket cap would close the connection
// instead of being answered. snapshot-helper.js checks the same number before
// sending (TestSnapshotHelper_SizeCapMatchesGo keeps the two in step).
const maxSnapshotScreenshotBase64 = 15 << 20

// handleSnapshotRequestMsg handles __devtool.snapshot baseline/compare
// requests from the page (serialised via asyncConn).
//
// Refused to an untrusted caller: a baseline is written to, and a comparison
// writes diff images into, the operator's baseline store on disk.
func (c *wsConnState) handleSnapshotRequestMsg(msg *wsMessage) {
	if c.refusePrivileged("snapshot_request") {
		writeControlResponse(c.asyncConn, "snapshot_response", getStringField(msg.Data, "request_id"), nil, privilegedRefusalMessage)
		return
	}
	c.spawnHandler(func() { c.ps.handleSnapshotRequest(c.asyncConn, msg.Data) })
}

// handleSnapshotRequest saves or compares one browser capture through the same
// snapshot.Manager storage the `snapshot` MCP tool uses, and replies with the
// real outcome as snapshot_response keyed by request_id: the saved Baseline
// for "baseline", the CompareResult for "compare", or an error.
func (ps *ProxyServer) handleSnapshotRequest(conn wsJSONWriter, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	reply := func(result interface{}, errMsg string) {
		writeControlResponse(conn, "snapshot_response", requestID, result, errMsg)
	}

	action := getStringField(data, "action")
	if action != "baseline" && action != "compare" {
		reply(nil, fmt.Sprintf("unknown snapshot action %q: expected baseline or compare", action))
		return
	}
	name := getStringField(data, "name")
	if name == "" {
		reply(nil, "baseline name is required")
		return
	}
	page, errMsg := snapshotPageFromRequest(getMapField(data, "page"))
	if errMsg != "" {
		reply(nil, errMsg)
		return
	}

	manager, err := snapshot.NewManagerForProject(ps.snapshotStoragePath, 0, ps.Path)
	if err != nil {
		reply(nil, fmt.Sprintf("snapshot storage unavailable: %v", err))
		return
	}

	if action == "baseline" {
		baseline, err := manager.CreateBaseline(name, []snapshot.PageCapture{page})
		if err != nil {
			reply(nil, fmt.Sprintf("failed to create baseline %q: %v", name, err))
			return
		}
		reply(baseline, "")
		return
	}
	result, err := manager.CompareToBaseline(name, []snapshot.PageCapture{page}, 0)
	if err != nil {
		reply(nil, fmt.Sprintf("failed to compare to baseline %q: %v", name, err))
		return
	}
	reply(result, "")
}

// snapshotPageFromRequest builds the PageCapture for one browser capture. Only
// inline screenshot_data is accepted: screenshot_path would let the page have
// the daemon read any file on disk into the baseline store.
func snapshotPageFromRequest(p map[string]interface{}) (snapshot.PageCapture, string) {
	data := getStringField(p, "screenshot_data")
	if data == "" {
		return snapshot.PageCapture{}, "page.screenshot_data is required (base64 PNG)"
	}
	if len(data) > maxSnapshotScreenshotBase64 {
		return snapshot.PageCapture{}, fmt.Sprintf("screenshot is %d bytes of base64, over the %d byte limit; capture a smaller page or viewport", len(data), maxSnapshotScreenshotBase64)
	}
	// 12 base64 characters decode to 9 bytes, enough to see the PNG signature
	// without decoding the whole image twice.
	head, err := base64.StdEncoding.DecodeString(data[:min(len(data), 12)])
	if err != nil || !bytes.HasPrefix(head, pngSignature) {
		return snapshot.PageCapture{}, "page.screenshot_data is not a base64-encoded PNG"
	}
	viewport := getMapField(p, "viewport")
	return snapshot.PageCapture{
		URL: getStringField(p, "url"),
		Viewport: snapshot.Viewport{
			Width:  getIntField(viewport, "width"),
			Height: getIntField(viewport, "height"),
		},
		ScreenshotData: data,
	}, ""
}
