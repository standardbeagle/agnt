package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// TestMergeUpstreamCarriesManualControlPlaneInputs pins the create path's
// upstream handling: the field is optional and byte-transparent when absent,
// folds into the artifact when the nested object is missing, tolerates a
// restatement of the same origin, and REJECTS two different origins rather than
// silently picking one — the origin decides what a viewer's browser loads.
func TestMergeUpstreamCarriesManualControlPlaneInputs(t *testing.T) {
	const base = `{"version":"v1","id":"demo","title":"Demo","steps":[]}`

	// Absent upstream: unchanged bytes, so existing callers are untouched.
	got, err := mergeUpstream(json.RawMessage(base), "")
	if err != nil {
		t.Fatalf("empty upstream: %v", err)
	}
	if string(got) != base {
		t.Fatalf("empty upstream rewrote the artifact: %s", got)
	}

	// Merged in when absent.
	got, err = mergeUpstream(json.RawMessage(base), "https://shop.example.com/cart")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var merged struct {
		ID       string `json:"id"`
		Upstream struct {
			URL string `json:"url"`
		} `json:"upstream"`
	}
	if err := json.Unmarshal(got, &merged); err != nil {
		t.Fatalf("decode merged: %v (%s)", err, got)
	}
	if merged.Upstream.URL != "https://shop.example.com/cart" {
		t.Fatalf("upstream not merged: %s", got)
	}
	if merged.ID != "demo" {
		t.Fatalf("merge lost an artifact field: %s", got)
	}

	// Identical restatement is fine.
	withUpstream := `{"version":"v1","id":"demo","title":"Demo","upstream":{"url":"https://a.example.com/"},"steps":[]}`
	if _, err := mergeUpstream(json.RawMessage(withUpstream), "https://a.example.com/"); err != nil {
		t.Fatalf("identical upstream rejected: %v", err)
	}

	// Two different origins is a loud rejection naming both.
	_, err = mergeUpstream(json.RawMessage(withUpstream), "https://b.example.com/")
	if err == nil {
		t.Fatalf("contradicting origins were silently resolved")
	}
	if !strings.Contains(err.Error(), "a.example.com") || !strings.Contains(err.Error(), "b.example.com") {
		t.Fatalf("rejection should name both origins: %v", err)
	}

	// A non-object artifact cannot take a merge and must say so.
	if _, err := mergeUpstream(json.RawMessage(`["not","an","object"]`), "https://a.example.com/"); err == nil {
		t.Fatalf("a non-object artifact accepted an upstream merge")
	}
}

// TestMergeUpstreamExplicitNullEqualsAbsent pins the item-4 null semantics: an
// `"upstream": null` key is treated exactly like an absent key — a nil
// *UpstreamConfig ("no upstream") downstream — so a non-empty param FILLS the
// declared-but-empty slot rather than being reported as a contradiction with a
// url of "". This FAILS on a revert to unmarshalling `null` into a zero struct.
func TestMergeUpstreamExplicitNullEqualsAbsent(t *testing.T) {
	const withNull = `{"version":"v1","id":"demo","title":"Demo","upstream":null,"steps":[]}`

	// Param empty + explicit null: passes through byte-transparent (null decodes
	// to no upstream downstream), just like absent.
	got, err := mergeUpstream(json.RawMessage(withNull), "")
	if err != nil {
		t.Fatalf("empty param over null upstream: %v", err)
	}
	if string(got) != withNull {
		t.Fatalf("empty param rewrote a null-upstream artifact: %s", got)
	}

	// Param present + explicit null: the origin is folded in, NOT rejected as a
	// contradiction. The pre-fix code unmarshalled null into url:"" and errored.
	got, err = mergeUpstream(json.RawMessage(withNull), "https://shop.example.com/cart")
	if err != nil {
		t.Fatalf("explicit null must accept a param, not contradict: %v", err)
	}
	var merged struct {
		ID       string `json:"id"`
		Upstream struct {
			URL string `json:"url"`
		} `json:"upstream"`
	}
	if err := json.Unmarshal(got, &merged); err != nil {
		t.Fatalf("decode merged over null: %v (%s)", err, got)
	}
	if merged.Upstream.URL != "https://shop.example.com/cart" {
		t.Fatalf("param not folded into null-upstream slot: %s", got)
	}
	if merged.ID != "demo" {
		t.Fatalf("merge lost an artifact field: %s", got)
	}
}

// TestPublishCreateAcceptsRawOpsArtifact pins that a variant set carrying the
// raw-content ops (setHTML/addStyle/addScript) plus an upstream survives the
// tool's own handling unaltered — the tool must not filter the op vocabulary it
// does not own. Validation of the ops belongs to the daemon-side P2 validators.
func TestPublishCreateAcceptsRawOpsArtifact(t *testing.T) {
	artifact := `{"version":"v1","id":"demo","title":"Demo",` +
		`"steps":[{"id":"s1","title":"One","advance":{"type":"auto","ms":1000}}],` +
		`"variantSet":{"version":"v1","id":"vs1","variants":[{"id":"v1","ops":[` +
		`{"op":"setHTML","selector":"#hero","html":"<b>hi</b>"},` +
		`{"op":"addStyle","css":"#hero{color:red}"},` +
		`{"op":"addScript","code":"console.log(1)"}]}]}}`

	got, err := mergeUpstream(json.RawMessage(artifact), "https://shop.example.com/cart")
	if err != nil {
		t.Fatalf("merge with raw ops: %v", err)
	}
	for _, op := range []string{"setHTML", "addStyle", "addScript", `"<b>hi</b>"`, "console.log(1)"} {
		if !strings.Contains(string(got), op) {
			t.Fatalf("raw op content %q did not survive: %s", op, got)
		}
	}
}

// TestDiscoverServeStoresSurfacedInList pins Part 3's Silent-Failure fix: a
// `publish serve` store the daemon does not manage is discovered and surfaced by
// `publish list`, with a pointer to its location and how to read its feedback —
// rather than being silently invisible.
func TestDiscoverServeStoresSurfacedInList(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	storeDir := filepath.Join(cache, "agnt", "publish-serve", "deadbeef")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	meta := []byte(`{"dir":"/home/dev/walkthroughs","addr":"127.0.0.1:8899"}`)
	if err := os.WriteFile(filepath.Join(storeDir, serveStoreMetaFile), meta, 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	stores := discoverServeStores()
	if len(stores) != 1 {
		t.Fatalf("expected 1 serve store, got %+v", stores)
	}
	if stores[0].Path != storeDir || stores[0].Dir != "/home/dev/walkthroughs" {
		t.Fatalf("serve store metadata not read: %+v", stores[0])
	}

	txt := renderText(t, renderPublish(PublishOutput{Action: "list", ServeStores: stores}, false))
	if !strings.Contains(txt, "Serve stores") || !strings.Contains(txt, storeDir) || !strings.Contains(txt, "/home/dev/walkthroughs") {
		t.Fatalf("list render omits the serve store: %q", txt)
	}
	if !strings.Contains(txt, "agnt publish feedback --store "+storeDir) {
		t.Fatalf("list render omits the feedback read hint: %q", txt)
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
