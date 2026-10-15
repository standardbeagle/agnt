package scripts

import (
	"strings"
	"testing"
)

// Always-on Go source guards for indicator.js panel behaviour PROVEN in the
// vitest + jsdom tier (internal/proxy/scripts/jstest, `make test-js`). Each
// names the JS test that proves it.

func indicatorSrc(t *testing.T) string {
	t.Helper()
	src := moduleScript["indicator"]
	if src == "" {
		t.Fatal("indicator module source not embedded")
	}
	return src
}

// funcBody returns the source of the named function: from its header to the
// next top-level (two-space indented) function header.
func funcBody(t *testing.T, src, header string) string {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("indicator.js: %q not found", header)
	}
	rest := src[start+len(header):]
	if end := strings.Index(rest, "\n  function "); end >= 0 {
		rest = rest[:end]
	}
	return header + rest
}

// TestIndicatorSend_KeepsMessageWhenNotSent — behaviour:
// jstest/indicator-panel-send.test.js. core.send returns false while the
// socket is not open; handleSend used to ignore that and clear the textarea and
// attachments and close the panel, silently losing the message.
func TestIndicatorSend_KeepsMessageWhenNotSent(t *testing.T) {
	body := funcBody(t, indicatorSrc(t), "function handleSend()")
	sendAt := strings.Index(body, "var sent = core.send('panel_message'")
	checkAt := strings.Index(body, "if (!sent) {")
	clearAt := strings.Index(body, "clearAttachments();")
	if sendAt < 0 || checkAt < sendAt || clearAt < checkAt {
		t.Fatal("handleSend must capture core.send's result and return on failure before clearing the message")
	}
}
