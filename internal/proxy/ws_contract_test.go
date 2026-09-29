package proxy

import (
	"regexp"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// Contract: every browser→daemon message type the shipped JS actually sends
// over the WebSocket must have a handler in wsMessageHandlers (ws_messages.go).
// A type with no handler is silently dropped by the read loop in
// ws_handler.go — see the "*_capture" / TLS-toast / responsive_request class
// of bugs this test is guarding against.
//
// Two send shapes appear in the scripts:
//   - literal:  core.send('sketch', {...})
//   - dynamic:  core.send(type + '_capture', {...}) where `type` comes from
//     an addAttachment(type, data) call site (indicator-data.js). The set of
//     concrete `<type>_capture` strings this can produce is exactly the set
//     of literal first arguments to `addAttachment(...)` across every script.
func TestWSMessageHandlers_CoverEveryShippedSendType(t *testing.T) {
	src := scripts.GetCombinedScript()

	literalSendRe := regexp.MustCompile(`core\.send\(\s*['"]([a-zA-Z0-9_\-]+)['"]`)
	addAttachmentRe := regexp.MustCompile(`addAttachment\(\s*['"]([a-zA-Z0-9_\-]+)['"]`)

	sent := map[string]bool{}
	for _, m := range literalSendRe.FindAllStringSubmatch(src, -1) {
		sent[m[1]] = true
	}
	for _, m := range addAttachmentRe.FindAllStringSubmatch(src, -1) {
		sent[m[1]+"_capture"] = true
	}

	if len(sent) == 0 {
		t.Fatalf("no core.send(...) call sites found in the combined script — regex or embed likely broken")
	}

	var missing []string
	for msgType := range sent {
		if _, ok := wsMessageHandlers[msgType]; !ok {
			missing = append(missing, msgType)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("shipped JS sends message type(s) with no wsMessageHandlers entry (silently dropped by ws_handler.go): %v", missing)
	}
}
