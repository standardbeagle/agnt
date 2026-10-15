package overlay

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// A terminal in bracketed-paste mode (the agent turns it on) wraps a paste in
// ESC [200~ … ESC [201~. With a panel open the overlay owns input, and the
// pasted text must arrive as one paste: read as keys, a 'q' in it closes the
// panel, a digit opens a script, and everything after that is typed into the
// agent.

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// typeInReads delivers parts as separate reads with gap between them.
func typeInReads(t *testing.T, write func([]byte) (int, error), gap time.Duration, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if _, err := write([]byte(p)); err != nil {
			t.Fatalf("writing %q: %v", p, err)
		}
		time.Sleep(gap)
	}
	time.Sleep(10 * testSplitGap)
}

func TestPasteIntoPanelIsNotReadAsKeys(t *testing.T) {
	// Gaps below and above the 50ms escape window: a paste split across reads
	// by a slow link must not fall out of paste mode between them.
	for _, gap := range []time.Duration{testSplitGap, 120 * time.Millisecond} {
		_, ov, pw, rec := splitRouter(t, StateMenu)
		ov.panelMode = true
		ov.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}

		typeInReads(t, pw.Write, gap, pasteStart+"echo q", "1x\r\nmore", " text"+pasteEnd)

		if got := childBytes(rec); len(got) != 0 {
			t.Errorf("gap %v: pasted text reached the child as %q", gap, got)
		}
		if ov.State() != StateMenu {
			t.Errorf("gap %v: overlay state = %v, want StateMenu (the paste closed the panel)", gap, ov.State())
		}
		ov.mu.Lock()
		if !ov.panelMode || ov.panelIndex != 0 {
			t.Errorf("gap %v: panelMode=%v panelIndex=%d, want the overview still showing", gap, ov.panelMode, ov.panelIndex)
		}
		ov.mu.Unlock()
	}
}

// The command palette takes typed text, so a paste goes into its input line —
// as text, never as Enter: a pasted newline must not run the command.
func TestPasteIntoCommandPaletteFillsTheInputLine(t *testing.T) {
	router, ov, pw, rec := splitRouter(t, StateMenu)
	ov.panelMode = true
	ov.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
	ov.mu.Lock()
	ov.commandInput = true
	ov.mu.Unlock()
	ctrl := &fakeProxyController{}
	router.scriptController = ctrl

	typeInReads(t, pw.Write, testSplitGap, pasteStart+"kill-po", "rt 3000\r\n"+pasteEnd)

	ov.mu.Lock()
	defer ov.mu.Unlock()
	if !ov.commandInput {
		t.Fatal("the pasted newline ran the palette command")
	}
	if ov.commandBuffer != "kill-port 3000" {
		t.Errorf("command buffer = %q, want %q", ov.commandBuffer, "kill-port 3000")
	}
	if got := childBytes(rec); len(got) != 0 {
		t.Errorf("pasted text reached the child as %q", got)
	}
}

// The config editor takes typed text, so a paste is inserted verbatim: line
// breaks kept, and indentation not doubled by the editor's auto-indent.
func TestPasteIntoConfigEditorInsertsTheText(t *testing.T) {
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	r.escReader = NewEscapeSequenceReader()
	r.overlay.renderer = NewRenderer(&bytes.Buffer{}, 80, 24)
	r.overlay.state.Store(int32(StateMenu))

	paste := pasteStart + "proxies {\r\n    dev {\r\n    }\r\n}\r\n" + pasteEnd
	for i := 0; i < len(paste); i++ {
		r.handleOverlayInput(paste[i])
	}

	if !r.isConfigPanel() {
		t.Fatal("the paste closed or switched away from the config panel")
	}
	want := "proxies {\n    dev {\n    }\n}\nscripts {\n}\n"
	if got := r.overlay.configEditor.Text(); got != want {
		t.Errorf("buffer = %q, want %q", got, want)
	}
	if line, col := r.overlay.configEditor.Cursor(); line != 4 || col != 0 {
		t.Errorf("cursor = %d:%d, want 4:0 (just after the paste)", line, col)
	}
}

func TestEscapeSequenceReader_PasteIsOneKey(t *testing.T) {
	r := NewEscapeSequenceReader()
	keys := feedAll(r, pasteStart+"q1")
	keys = append(keys, feedAll(r, "\x1b[Ax"+pasteEnd)...)
	if len(keys) != 1 || keys[0] != pasteKeyPrefix+"q1\x1b[Ax" {
		t.Fatalf("keys = %q, want one paste carrying the text", keys)
	}
	if r.IsPending() {
		t.Error("reader still pending after the paste ended")
	}
	if key, complete := r.Feed('q'); !complete || key != "q" {
		t.Errorf("next key after a paste = %q/%v, want q/true", key, complete)
	}
}

// A paste larger than the cap is dropped whole, never delivered truncated and
// never leaked as keys.
func TestEscapeSequenceReader_OversizedPasteIsDropped(t *testing.T) {
	r := NewEscapeSequenceReader()
	keys := feedAll(r, pasteStart+strings.Repeat("q", pasteMaxLen+10)+pasteEnd)
	if len(keys) != 0 {
		t.Fatalf("oversized paste produced %d keys (first %.20q)", len(keys), keys[0])
	}
	if r.IsPending() {
		t.Error("reader still pending after an oversized paste ended")
	}
}
