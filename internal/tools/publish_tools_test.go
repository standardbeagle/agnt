package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-sdk/mcp"
)

// TestPublishHandlerValidation pins the publish tool's input guards and its
// no-daemon behavior: it fails loud (IsError) rather than pretending success,
// and it never reaches the daemon without a valid action.
func TestPublishHandlerValidation(t *testing.T) {
	h := makePublishHandler(nil) // nil daemon tools
	cases := []struct {
		name  string
		input PublishInput
	}{
		{"missing action", PublishInput{}},
		{"unknown action still needs client", PublishInput{Action: "bogus"}},
		{"create needs client", PublishInput{Action: "create", Walkthrough: json.RawMessage(`{}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _, err := h(context.Background(), nil, tc.input)
			if err != nil {
				t.Fatalf("handler returned transport error: %v", err)
			}
			if res == nil || !res.IsError {
				t.Fatalf("expected an error result, got %+v", res)
			}
		})
	}
}

// TestPublishRenderNeverLeaksTokenInReads pins INV-9 at the render layer: the
// status/list views carry only a hash prefix and never a plaintext token, and
// only create/rotate surface the once-shown token.
func TestPublishRenderNeverLeaksTokenInReads(t *testing.T) {
	share := protocol.PublishShareInfo{
		ID:              "share-1",
		Title:           "Demo",
		Steps:           3,
		TokenHashPrefix: "deadbeef",
		CreatedAt:       "2026-07-13T00:00:00Z",
	}

	statusOut := PublishOutput{Action: "status", ID: share.ID, Share: &share}
	txt := renderText(t, renderPublish(statusOut, false))
	if strings.Contains(txt, "token") {
		t.Fatalf("status render mentions a token: %q", txt)
	}
	if !strings.Contains(txt, "deadbeef") {
		t.Fatalf("status render should show the hash prefix")
	}

	listOut := PublishOutput{Action: "list", Shares: []protocol.PublishShareInfo{share}}
	if strings.Contains(renderText(t, renderPublish(listOut, false)), "token") {
		t.Fatalf("list render mentions a token")
	}

	// create is the ONE place the token appears.
	createOut := PublishOutput{Action: "create", ID: "share-1", Token: "SECRET-TOKEN", ShareURL: "/s/SECRET-TOKEN"}
	ctxt := renderText(t, renderPublish(createOut, false))
	if !strings.Contains(ctxt, "SECRET-TOKEN") {
		t.Fatalf("create render should show the token once")
	}
	if !strings.Contains(ctxt, "ONCE") {
		t.Fatalf("create render should warn the token is shown once")
	}
}

// TestPublishFeedbackActionValidation pins that the feedback read action needs a
// share id and fails loud without a daemon client.
func TestPublishFeedbackActionValidation(t *testing.T) {
	h := makePublishHandler(nil)
	res, _, err := h(context.Background(), nil, PublishInput{Action: "feedback"})
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("feedback with no client must be an error result, got %+v", res)
	}
}

// TestPublishFeedbackRenderNoToken pins that the feedback render shows the rows +
// observability counts and never a token (feedback rows structurally carry
// none; this guards against a future field leaking one into the view).
func TestPublishFeedbackRenderNoToken(t *testing.T) {
	out := PublishOutput{
		Action:  "feedback",
		ID:      "share-1",
		Total:   2,
		Dropped: 3,
		Feedback: []protocol.PublishFeedbackRow{
			{ID: "row-1", CreatedAt: "2026-07-13T00:00:00Z", Body: `{"message":"nice"}`},
		},
		NextCursor: "row-1",
	}
	txt := renderText(t, renderPublish(out, false))
	if strings.Contains(txt, "token") {
		t.Fatalf("feedback render mentions a token: %q", txt)
	}
	if !strings.Contains(txt, "total=2") || !strings.Contains(txt, "dropped=3") {
		t.Fatalf("feedback render should show observability counts: %q", txt)
	}
	if !strings.Contains(txt, "nice") {
		t.Fatalf("feedback render should show the row body")
	}
	if !strings.Contains(txt, "next_cursor: row-1") {
		t.Fatalf("feedback render should show the pagination cursor")
	}
}

func renderText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return tc.Text
}
