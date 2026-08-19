package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

// Guards for the P5 public walkthrough PLAYER's typewriter reveal
// (walkthrough-viewer.js revealBody). The reveal walks the narration
// character-by-character; before this fix it stepped the cursor by a fixed 2
// UTF-16 code units and rendered text.slice(0, i). When that cursor landed
// between the two halves of a surrogate pair, the frame ended on a LONE high
// surrogate, which the browser paints as U+FFFD (the replacement box) — visible
// mojibake on astral-plane content (emoji, rare CJK) that flickers away one
// frame later. Same "never split a character" class as truncateToBytes, a
// different function.
//
// The fix keeps the cursor a CODE-UNIT cursor (text.length < 2 and i >=
// text.length stay in code units, indexing the same string); only the step
// BOUNDARY changes: each tick advances to the next grapheme-cluster boundary via
// the SHARED nextClusterEnd walker (the same one truncateToBytes now uses), so a
// frame never ends mid-surrogate-pair or mid-cluster.

// TestPlayerRevealAdvancesOnClusterBoundary is the always-on source guard (no
// node required): the reveal must advance via the shared cluster walker, and
// that walker must be shared with truncateToBytes rather than a fourth copy of
// the Unicode-traversal logic.
func TestPlayerRevealAdvancesOnClusterBoundary(t *testing.T) {
	js := walkthroughViewerJS

	// The shared cluster-boundary walker must exist...
	if !strings.Contains(js, "function nextClusterEnd(s, start)") {
		t.Error("walkthrough-viewer.js must carry the shared cluster-boundary walker function nextClusterEnd(s, start)")
	}
	// ...and truncateToBytes must REUSE it (not keep its own parallel copy of the
	// codePointAt/ZWJ/regional-indicator traversal — this file already had three
	// near-identical Unicode walks; a fourth is the duplication we are avoiding).
	if !strings.Contains(extractJSFunc(t, js, "function truncateToBytes(s, maxBytes)"), "nextClusterEnd(s, i)") {
		t.Error("truncateToBytes must reuse nextClusterEnd rather than re-deriving cluster traversal inline")
	}

	reveal := extractJSFunc(t, js, "function revealBody(text)")
	// revealBody must advance the cursor through the shared walker...
	if !strings.Contains(reveal, "nextClusterEnd(text, i)") {
		t.Error("revealBody must advance the reveal cursor to a grapheme-cluster boundary via nextClusterEnd(text, i) — a bare i += 2 splits surrogate pairs into U+FFFD")
	}
	// ...and must NOT step by a raw two code units, which is the split-surrogate bug.
	if strings.Contains(reveal, "i += 2;") {
		t.Error("revealBody still steps the cursor by a raw 2 code units (i += 2;) — that lands mid-surrogate-pair on astral content")
	}
	// The code-unit cursor semantics are unchanged: both terminal checks index the
	// same string in code units and must stay that way.
	if !strings.Contains(reveal, "i >= text.length") {
		t.Error("revealBody must keep its code-unit completion check (i >= text.length) — the cursor stays a code-unit cursor; only the step boundary changed")
	}
	if !strings.Contains(reveal, "text.length < 2") {
		t.Error("revealBody must keep the code-unit short-text guard (text.length < 2)")
	}
}

// TestPlayerRevealNoSplitSurrogateFrames drives the player's OWN revealBody
// source under node (helpers, the shared walker and revealBody extracted
// verbatim) behind stub DOM/timer globals, collects EVERY frame written to
// bodyEl.textContent, and asserts no frame is mojibake: none ends on a lone
// surrogate and none contains U+FFFD. Driven with astral-plane emoji and a ZWJ
// sequence — exactly the content the raw i += 2 stride split.
//
// Per the walkthrough_player precedent (TestPlayerByteTruncationIsClusterSafe),
// running shipped JS from Go is OFF by default so the suite's greenness never
// depends on a node install:
//
//	AGNT_JS_RUNTIME_TESTS=1 go test ./internal/proxy/scripts/ -run TestPlayerRevealNoSplitSurrogateFrames
func TestPlayerRevealNoSplitSurrogateFrames(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") == "" {
		t.Skip("SKIPPING js-runtime tier: set AGNT_JS_RUNTIME_TESTS=1 to drive revealBody under node (the always-on source guard in TestPlayerRevealAdvancesOnClusterBoundary still ran)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("AGNT_JS_RUNTIME_TESTS is set but node is not on PATH: %v", err)
	}

	cases := []struct {
		name string
		text string
	}{
		// Astral-plane emoji: 2 code units each. A raw i += 2 stride can land
		// exactly between the high and low surrogate on the odd-length prefixes.
		{"astral-emoji-run", strings.Repeat("\U0001F600", 12)},
		// Mixed ASCII + astral: shifts the pair boundaries off the even stride so
		// the cursor lands mid-pair on many ticks unless snapped.
		{"ascii-then-emoji", "hi " + strings.Repeat("\U0001F680", 8) + " bye"},
		// ZWJ family sequence: cutting anywhere inside must not surface a lone
		// surrogate or a dangling joiner in any frame.
		{"zwj-family", strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 3)},
		// Rare astral CJK (Plane 2) — non-emoji astral content hits the same path.
		{"astral-cjk", strings.Repeat("\U00020BB7", 10)},
		// Regional-indicator flags: each half is astral; an odd cut splits the flag.
		{"regional-flags", strings.Repeat("\U0001F1EF\U0001F1F5", 6)},
	}

	type driverCase struct {
		Text string `json:"text"`
	}
	payload := make([]driverCase, len(cases))
	for i, c := range cases {
		payload[i] = driverCase{Text: c.text}
	}
	in, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}

	cmd := exec.Command(node, "-e", revealFrameDriver(t))
	cmd.Stdin = strings.NewReader(string(in))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed: %v\n%s", err, raw)
	}
	var got struct {
		Frames [][]string `json:"frames"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node output %q: %v", raw, err)
	}
	if len(got.Frames) != len(cases) {
		t.Fatalf("node returned %d frame-sets for %d cases", len(got.Frames), len(cases))
	}

	for i, c := range cases {
		frames := got.Frames[i]
		t.Run(c.name, func(t *testing.T) {
			if len(frames) == 0 {
				t.Fatalf("reveal produced no frames for %q", c.name)
			}
			sawSplit := false
			for fi, f := range frames {
				// A lone surrogate cannot round-trip UTF-8; Go's JSON decoder maps
				// it to U+FFFD, so a split pair shows up either as a U+FFFD in the
				// decoded frame or a trailing RuneError. Both are the defect.
				if strings.ContainsRune(f, utf8.RuneError) {
					t.Errorf("frame %d contains U+FFFD (a surrogate pair was split): %q", fi, f)
					sawSplit = true
				}
				// Every frame must be a prefix of the full text and decode cleanly
				// to whole code points (no partial cluster tail).
				if !strings.HasPrefix(c.text, f) {
					t.Errorf("frame %d %q is not a prefix of the source — reveal must only extend, never rewrite", fi, f)
				}
			}
			// The final frame must be the complete text.
			if last := frames[len(frames)-1]; last != c.text {
				t.Errorf("final reveal frame must equal the full text; got %q", last)
			}
			if sawSplit {
				t.Errorf("case %q surfaced at least one split-surrogate frame — the reveal stride split a character", c.name)
			}
		})
	}
}

// revealFrameDriver builds a node script out of walkthrough-viewer.js's OWN
// source — isExtender, isRegionalIndicator, the shared nextClusterEnd walker,
// and revealBody, all extracted verbatim — behind stub DOM/timer globals that
// capture every bodyEl.textContent assignment as a frame. Driving the real
// revealBody (not a re-implementation of its stride) is what gives the test
// teeth: reverting the stride to i += 2 re-splits pairs here.
func revealFrameDriver(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, header := range []string{
		"function isExtender(cp)",
		"function isRegionalIndicator(cp)",
		"function nextClusterEnd(s, start)",
		"function revealBody(text)",
	} {
		b.WriteString(extractJSFunc(t, walkthroughViewerJS, header))
		b.WriteString("\n")
	}
	// Stub the closure globals revealBody references, plus a synchronous timer
	// registry we pump to completion. bodyEl captures each textContent write.
	b.WriteString(`
var reducedMotion = false;
var destroyed = false;
var intervals = [];
var _frames = [];
var bodyEl = {};
Object.defineProperty(bodyEl, 'textContent', {
  set: function (v) { _frames.push(String(v)); },
  get: function () { return _frames.length ? _frames[_frames.length - 1] : ''; }
});
var _timers = {};
var _nextId = 1;
function setInterval(fn) { var id = _nextId++; _timers[id] = fn; return id; }
function clearInterval(id) { delete _timers[id]; }
function pump() {
  var guard = 0;
  while (Object.keys(_timers).length && guard < 1000000) {
    guard++;
    var ids = Object.keys(_timers);
    for (var j = 0; j < ids.length; j++) {
      var f = _timers[ids[j]];
      if (typeof f === 'function') { f(); }
    }
  }
}
function framesFor(text) {
  _frames = [];
  intervals = [];
  destroyed = false;
  revealBody(text);
  pump();
  return _frames;
}
var cases = JSON.parse(require('fs').readFileSync(0, 'utf8'));
process.stdout.write(JSON.stringify({
  frames: cases.map(function (c) { return framesFor(c.text); })
}));
`)
	return b.String()
}
