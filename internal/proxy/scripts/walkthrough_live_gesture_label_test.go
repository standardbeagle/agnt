package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The LIVE-plane walkthrough runner (walkthrough.js) caps an author-supplied
// gesture_label against publish.MaxGestureLabelLength — a Go `len()` bound, i.e.
// UTF-8 BYTES (internal/publish/validate.go:205, "gesture_label exceeds %d
// bytes"). JS `.length` counts UTF-16 CODE UNITS, and UTF-8 byte length is >=
// code-unit count for every character (CJK 3 bytes / 1 unit, astral emoji 4
// bytes / 2 units). So a code-unit cap is LOOSER than the Go byte cap: it admits
// labels the Go validator rejects (30 CJK chars = 30 units, under 64, but 90
// bytes, over 64). This is the same cap-UNIT drift already fixed in
// variant-engine.js (commit 34abd80c) and walkthrough-viewer.js
// (22ba23f2/7aa5595d) — the THIRD file in the class. See
// TestVariantEngineCapsAreByteDenominated for the sibling source guard.
//
// Note on direction: the task framed this as a "false rejection" of legitimate
// CJK/emoji, but the arithmetic is the reverse — a code-unit cap is never
// tighter than the byte cap, so pre-fix the live plane over-ACCEPTS payloads Go
// refuses. The fix (byte-denomination, matching Go) is identical either way, and
// criterion 3's behavioral spec ("under the limit in code units but over it in
// bytes must be refused") is what this pins.

// TestLiveWalkthroughGestureLabelCapIsByteDenominated is the always-on
// source-level guard: reverting the fix to a `.length` comparison, or dropping
// the utf8Len helper, fails here in the default suite (no node required).
func TestLiveWalkthroughGestureLabelCapIsByteDenominated(t *testing.T) {
	js := walkthroughJS

	const (
		byteEd = "if (utf8Len(s.gesture_label) > MAX_GESTURE_LABEL)"
		unitEd = "if (s.gesture_label.length > MAX_GESTURE_LABEL)"
	)
	if !strings.Contains(js, byteEd) {
		t.Errorf("walkthrough.js gesture_label cap must compare UTF-8 bytes (%q), mirroring internal/publish/validate.go:205 len(s.GestureLabel) > MaxGestureLabelLength", byteEd)
	}
	if strings.Contains(js, unitEd) {
		t.Errorf("walkthrough.js gesture_label cap compares UTF-16 code units (%q) against a byte-denominated Go limit — a code-unit cap over-accepts (3x gap for CJK, 4x for emoji)", unitEd)
	}

	// The constant must carry the Go value; the two sibling files and the Go
	// side keep 64 in lockstep (limits.go:44).
	if !strings.Contains(js, "var MAX_GESTURE_LABEL = 64;") {
		t.Error("walkthrough.js must declare var MAX_GESTURE_LABEL = 64; (publish.MaxGestureLabelLength)")
	}

	// The helper must be the SAME spelling variant-engine.js / walkthrough-viewer.js
	// use, so the mirrors cannot drift into two different notions of "length".
	for _, tok := range []string{
		"function utf8Len(s)",
		"new TextEncoder().encode(s).length",
	} {
		if !strings.Contains(js, tok) {
			t.Errorf("walkthrough.js must carry %q (the shared byte-length helper spelling)", tok)
		}
	}

	// The unit claim must live next to the cap: a bare "Mirrors publish.Max…"
	// with no unit is exactly what let the sibling mirrors drift unnoticed.
	if !strings.Contains(js, "compared in UTF-8 BYTES via utf8Len()") {
		t.Error("walkthrough.js must state that the gesture_label cap is byte-denominated — a unitless 'mirrors' claim is what let this drift")
	}
}

// TestLiveWalkthroughGestureLabelByteRefused drives walkthrough.js's OWN
// normalizeScript under node (constants + utf8Len + normalizeScript extracted
// verbatim) so the refuse/accept behavior goes through the real cap wiring.
//
// Per the walkthrough_player precedent (TestPlayerByteTruncationIsClusterSafe),
// running shipped JS from Go is OFF by default so the suite's greenness never
// depends on a node install:
//
//	AGNT_JS_RUNTIME_TESTS=1 go test ./internal/proxy/scripts/ -run TestLiveWalkthroughGestureLabelByteRefused
//
// Paired present/absent: a label under the limit in code units but over it in
// bytes must be REFUSED (throws); one under both must be ACCEPTED.
func TestLiveWalkthroughGestureLabelByteRefused(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") == "" {
		t.Skip("SKIPPING js-runtime tier: set AGNT_JS_RUNTIME_TESTS=1 to drive walkthrough.js normalizeScript under node (the always-on source guard in TestLiveWalkthroughGestureLabelCapIsByteDenominated still ran)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("AGNT_JS_RUNTIME_TESTS is set but node is not on PATH: %v", err)
	}

	cases := []struct {
		name          string
		label         string
		expectRefused bool
	}{
		// 30 CJK: 30 code units (under 64), 90 bytes (over 64). The regression this
		// tier exists for — pre-fix the code-unit cap admits it; Go rejects it.
		{"cjk-30-over-bytes-under-units", strings.Repeat("中", 30), true},
		// 20 CJK: 20 units, 60 bytes — under both, accepted on both planes.
		{"cjk-20-under-both", strings.Repeat("中", 20), false},
		// 20 astral emoji: 40 code units (under 64), 80 bytes (over 64) — refused.
		{"emoji-20-over-bytes-under-units", strings.Repeat("\U0001F600", 20), true},
		// 15 astral emoji: 30 units, 60 bytes — under both, accepted.
		{"emoji-15-under-both", strings.Repeat("\U0001F600", 15), false},
		// Plain ASCII exactly at the ceiling and one over — the byte and unit counts
		// agree here, so both planes treat these identically.
		{"ascii-64-at-ceiling", strings.Repeat("x", 64), false},
		{"ascii-65-over-ceiling", strings.Repeat("x", 65), true},
	}

	type driverCase struct {
		Label string `json:"label"`
	}
	payload := make([]driverCase, len(cases))
	for i, c := range cases {
		payload[i] = driverCase{Label: c.label}
	}
	in, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}

	cmd := exec.Command(node, "-e", liveGestureLabelDriver(t))
	cmd.Stdin = strings.NewReader(string(in))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed: %v\n%s", err, raw)
	}
	var got struct {
		Limit   int    `json:"limit"`
		Refused []bool `json:"refused"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node output %q: %v", raw, err)
	}
	if got.Limit != 64 {
		t.Errorf("MAX_GESTURE_LABEL is %d, want 64 (publish.MaxGestureLabelLength)", got.Limit)
	}
	if len(got.Refused) != len(cases) {
		t.Fatalf("node returned %d results for %d cases", len(got.Refused), len(cases))
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			units := len([]rune(c.label))
			bytes := len(c.label)
			if got.Refused[i] != c.expectRefused {
				verb := "accepted"
				if c.expectRefused {
					verb = "refused"
				}
				t.Errorf("gesture_label of %d code units / %d bytes must be %s to match Go's %d-byte limit, but normalizeScript %s it",
					units, bytes, verb, got.Limit, map[bool]string{true: "refused", false: "accepted"}[got.Refused[i]])
			}
		})
	}
}

// liveGestureLabelDriver builds a node script out of walkthrough.js's OWN
// source — the MAX_GESTURE_LABEL / VALID_GESTURES declarations, the utf8Len
// helper, and normalizeScript, all extracted verbatim — plus a stdin/stdout
// harness that reports whether each label was refused (threw). Driving the real
// normalizeScript (not utf8Len alone) exercises the actual cap wiring.
func liveGestureLabelDriver(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, decl := range []string{
		"var VALID_GESTURES",
		"var MAX_GESTURE_LABEL",
	} {
		b.WriteString(extractJSLine(t, walkthroughJS, decl))
		b.WriteString("\n")
	}
	for _, header := range []string{
		"function utf8Len(s)",
		"function normalizeScript(script)",
	} {
		b.WriteString(extractJSFunc(t, walkthroughJS, header))
		b.WriteString("\n")
	}
	b.WriteString(`
function refused(label) {
  try {
    normalizeScript({ id: 't', steps: [{ title: 't', target: '#a', gesture: 'click', gesture_label: label }] });
    return false;
  } catch (e) {
    return true;
  }
}
var cases = JSON.parse(require('fs').readFileSync(0, 'utf8'));
process.stdout.write(JSON.stringify({
  limit: MAX_GESTURE_LABEL,
  refused: cases.map(function (c) { return refused(c.label); })
}));
`)
	return b.String()
}
