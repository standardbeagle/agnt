package scripts

import (
	"strings"
	"testing"
)

// TestCaptureOncloneSwapsIframeForImage guards the iframe pre-render in both
// capture paths (__devtool.screenshot in api.js and __devtool.snapshot in
// snapshot-helper.js). html2canvas paints an <iframe> as an empty box, so each
// path renders the frame's content itself and, in html2canvas's onclone hook,
// replaces the cloned <iframe> with an <img> of it. Building the <img> without
// the replaceChild leaves the capture exactly as blank as before; api.js lost
// that line once while gaining the transform handling.
//
// Source guard only: the swap itself is proven against a real cloned document
// in jstest/screenshot-iframes.test.js (make test-js).
func TestCaptureOncloneSwapsIframeForImage(t *testing.T) {
	const hookStart = "canvasOpts.onclone = function"
	const hookEnd = "if (origOnclone)"
	for _, module := range []string{"api", "snapshot-helper"} {
		src := moduleScript[module]
		if src == "" {
			t.Fatalf("%s module source not embedded", module)
		}
		start := strings.Index(src, hookStart)
		if start < 0 {
			t.Fatalf("%s: onclone hook %q not found", module, hookStart)
		}
		end := strings.Index(src[start:], hookEnd)
		if end < 0 {
			t.Fatalf("%s: end of onclone hook %q not found", module, hookEnd)
		}
		hook := src[start : start+end]
		if !strings.Contains(hook, "createElement('img')") {
			t.Fatalf("%s: onclone hook no longer builds the replacement <img>", module)
		}
		if !strings.Contains(hook, "replaceChild(img, cf)") {
			t.Errorf("%s: onclone hook builds the <img> but never swaps it in for the cloned <iframe>; captured iframes render blank", module)
		}
	}
}
