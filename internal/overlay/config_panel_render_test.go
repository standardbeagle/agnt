package overlay

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

// ansiSeq matches the escape sequences the renderer interleaves with text.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// plain strips escapes so a text assertion is not defeated by them. The cursor
// cell in particular splits the line it sits on ("s" + Reverse + "cripts {"),
// which silently breaks every naive Contains on the visible text.
func plain(out string) string { return ansiSeq.ReplaceAllString(out, "") }

// Renderer tests for the .agnt.kdl editor panel. The model tests cover what
// editing does to the buffer; these cover what the developer actually sees —
// which line the cursor is on, whether the buffer would save, and that a long
// or scrolled file stays inside the panel.

func renderEditor(t *testing.T, e *ConfigEditor, width, maxRows int) string {
	t.Helper()
	var buf bytes.Buffer
	r := NewRenderer(&buf, width+10, maxRows+10)
	r.drawConfigPanelContent(1, 1, width, maxRows, e)
	return buf.String()
}

func editorFor(t *testing.T, body string) *ConfigEditor {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.AgntConfigFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	e, err := NewConfigEditor(path)
	if err != nil {
		t.Fatalf("NewConfigEditor: %v", err)
	}
	return e
}

func TestDrawConfigPanel_ShowsBufferWithLineNumbers(t *testing.T) {
	e := editorFor(t, "scripts {\n    dev {\n    }\n}\n")
	e.MoveCursor(2, 0) // off the first line, so the cursor cell is not in the way
	out := plain(renderEditor(t, e, 60, 10))

	for _, want := range []string{"scripts {", "dev {"} {
		if !strings.Contains(out, want) {
			t.Errorf("buffer line %q not drawn:\n%s", want, out)
		}
	}
	// The gutter is what lets a parse error naming a line be found by eye.
	for _, n := range []string{"   1 ", "   2 ", "   3 "} {
		if !strings.Contains(out, n) {
			t.Errorf("line number %q missing from the gutter:\n%s", n, out)
		}
	}
}

// TestDrawConfigPanel_CursorMarksTheCellUnderIt: the overlay hides the
// terminal's own cursor while a panel is up, so an editor that does not draw
// its own leaves the developer typing blind.
func TestDrawConfigPanel_CursorMarksTheCellUnderIt(t *testing.T) {
	e := editorFor(t, "scripts {\n}\n")
	e.MoveCursor(0, 3) // the 'i' of "scripts"

	out := renderEditor(t, e, 60, 10)
	if !strings.Contains(out, Reverse+"i"+Reset) {
		t.Errorf("cursor cell not inverted over the character it sits on:\n%q", out)
	}
	// Exactly one cell is the cursor; a second would mean every line is drawing
	// one, which is worse than none.
	if got := strings.Count(out, Reverse); got != 1 {
		t.Errorf("drew %d cursor cells, want 1:\n%q", got, out)
	}
}

func TestDrawConfigPanel_CursorPastEndOfLineDrawsASpace(t *testing.T) {
	// At end of line there is no character to invert, and drawing nothing would
	// lose the cursor entirely on every empty line.
	e := editorFor(t, "ab\n\n")
	e.MoveCursor(1, 0) // the empty second line

	out := renderEditor(t, e, 60, 10)
	if !strings.Contains(out, Reverse+" "+Reset) {
		t.Errorf("no cursor drawn on an empty line:\n%q", out)
	}
}

func TestDrawConfigPanel_ReportsWhetherTheBufferWouldSave(t *testing.T) {
	e := editorFor(t, "scripts {\n}\n")

	clean := plain(renderEditor(t, e, 60, 10))
	if !strings.Contains(clean, "KDL: ok") {
		t.Errorf("a parsing buffer is not reported as ok:\n%s", clean)
	}
	if !strings.Contains(clean, "saved") {
		t.Errorf("an unedited buffer should not read as edited:\n%s", clean)
	}

	e.MoveToLineEnd()
	e.Insert(" {") // unbalanced

	broken := plain(renderEditor(t, e, 60, 10))
	if strings.Contains(broken, "KDL: ok") {
		t.Errorf("a buffer that cannot save still reads as ok:\n%s", broken)
	}
	if !strings.Contains(broken, "KDL: ") {
		t.Errorf("parse failure not surfaced at all:\n%s", broken)
	}
	if !strings.Contains(broken, "edited") {
		t.Errorf("unsaved changes not marked:\n%s", broken)
	}
}

func TestDrawConfigPanel_ShowsTheSaveMessage(t *testing.T) {
	e := editorFor(t, "scripts {\n}\n")
	e.MoveCursor(1, 0)
	e.MoveToLineEnd()
	e.Insert("\n// a comment") // a valid edit, so the save actually happens
	if err := e.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out := plain(renderEditor(t, e, 100, 10))
	if !strings.Contains(out, "saved") {
		t.Errorf("save confirmation not shown:\n%s", out)
	}
}

// TestDrawConfigPanel_ReservesTheStatusRow pins the agreement between the
// scroll window and the drawing loop. If the buffer used the full panel height
// the last line would be drawn underneath the status line, and a cursor there
// would look lost.
func TestDrawConfigPanel_ReservesTheStatusRow(t *testing.T) {
	body := ""
	for i := 1; i <= 20; i++ {
		body += fmt.Sprintf("// line %d\n", i)
	}
	e := editorFor(t, body)

	const maxRows = 6
	out := plain(renderEditor(t, e, 60, maxRows))

	// Count gutter entries rather than line text: the gutter is fixed-width, so
	// "   1 " cannot also match line 10 the way "// line 1" would.
	drawn := 0
	for i := 1; i <= 20; i++ {
		if strings.Contains(out, fmt.Sprintf("%4d ", i)) {
			drawn++
		}
	}
	if drawn != maxRows-1 {
		t.Errorf("drew %d buffer lines in a %d-row panel, want %d (one row is the status line)", drawn, maxRows, maxRows-1)
	}
	if !strings.Contains(out, "KDL: ok") {
		t.Errorf("status line missing from a full panel:\n%s", out)
	}
}

func TestDrawConfigPanel_ScrollsToKeepTheCursorVisible(t *testing.T) {
	body := ""
	for i := 1; i <= 40; i++ {
		body += fmt.Sprintf("// line %d\n", i)
	}
	e := editorFor(t, body)
	e.MoveCursor(35, 0)

	out := plain(renderEditor(t, e, 60, 8))
	if !strings.Contains(out, "// line 36") { // cursor line (0-based 35)
		t.Errorf("cursor line not visible after scrolling:\n%s", out)
	}
	if strings.Contains(out, "   1 ") {
		t.Errorf("panel still showing the top of the file after scrolling:\n%s", out)
	}
}

func TestDrawConfigPanel_TruncatesLongLinesInsteadOfWrapping(t *testing.T) {
	// A wrapped line would push everything below it down and desynchronise the
	// gutter from the buffer.
	long := "// " + strings.Repeat("x", 300)
	e := editorFor(t, long+"\n")

	out := plain(renderEditor(t, e, 40, 6))
	for _, line := range strings.Split(out, "\n") {
		if strings.Count(line, "x") > 40 {
			t.Errorf("line drawn wider than the panel (%d x's):\n%s", strings.Count(line, "x"), line)
		}
	}
}

func TestDrawConfigPanel_NilEditorSaysSoInsteadOfPanicking(t *testing.T) {
	out := plain(renderEditor(t, nil, 60, 10))
	if !strings.Contains(out, "no config loaded") {
		t.Errorf("a nil editor should render an explanation, got:\n%q", out)
	}
}

// TestDrawPanelView_ConfigPanelTitleAndHint checks the whole-panel path: the
// title names the file, and the hint lists the editor's keys rather than the
// panel-browser bindings, which do not apply while the editor has the keyboard.
func TestDrawPanelView_ConfigPanelTitleAndHint(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 100, 30)
	panels := []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "config", ID: "__config", Label: "config"},
	}
	e := editorFor(t, "scripts {\n}\n")

	r.DrawPanelView(panels, 1, Status{}, 0, false, "", 0, false, OverviewActions{ConfigEditor: e})

	out := plain(buf.String())
	if !strings.Contains(out, ".agnt.kdl") {
		t.Errorf("panel title does not name the file:\n%s", out)
	}
	if !strings.Contains(out, "^S Save") || !strings.Contains(out, "Esc Close") {
		t.Errorf("hint does not describe the editor keys:\n%s", out)
	}
	if strings.Contains(out, "x Close stopped") {
		t.Errorf("hint still offers panel-browser bindings the editor swallows:\n%s", out)
	}
	if !strings.Contains(out, "scripts {") {
		t.Errorf("buffer not drawn through DrawPanelView:\n%s", out)
	}
}

func TestRenderConfigLine(t *testing.T) {
	// The pure helper, checked directly: cursor placement is off-by-one bait.
	if got := renderConfigLine("abc", 10, false, 0); got != "abc" {
		t.Errorf("line without a cursor was altered: %q", got)
	}
	if got, want := renderConfigLine("abc", 10, true, 1), "a"+Reverse+"b"+Reset+"c"; got != want {
		t.Errorf("cursor in the middle:\n got %q\nwant %q", got, want)
	}
	if got, want := renderConfigLine("abc", 10, true, 3), "abc"+Reverse+" "+Reset; got != want {
		t.Errorf("cursor at end of line:\n got %q\nwant %q", got, want)
	}
	if got := renderConfigLine("abcdef", 3, false, 0); got != "abc" {
		t.Errorf("line not truncated to the panel width: %q", got)
	}
}
