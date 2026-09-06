package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

func editorOn(t *testing.T, body string) (*ConfigEditor, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.AgntConfigFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	e, err := NewConfigEditor(path)
	if err != nil {
		t.Fatalf("NewConfigEditor: %v", err)
	}
	return e, path
}

func TestConfigEditor_OpensMissingFileOnTheDocumentedTemplate(t *testing.T) {
	// An empty buffer would tell the developer nothing about which keys exist,
	// so a project with no config opens on the same template `agnt init` writes.
	path := filepath.Join(t.TempDir(), config.AgntConfigFileName)
	e, err := NewConfigEditor(path)
	if err != nil {
		t.Fatalf("NewConfigEditor: %v", err)
	}
	if !strings.Contains(e.Text(), "scripts {") {
		t.Errorf("missing file did not open on the default template:\n%s", e.Text())
	}
	if e.Status() == "" || !strings.Contains(e.Status(), path) {
		t.Errorf("status should say the save will create the file: %q", e.Status())
	}
	if e.Dirty() {
		t.Error("a freshly opened buffer is not dirty")
	}
}

func TestConfigEditor_InsertAndBackspace(t *testing.T) {
	e, _ := editorOn(t, "scripts {\n}\n")
	e.MoveCursor(0, 3) // inside "scripts"
	e.Insert("X")
	if got := e.Lines()[0]; got != "scrXipts {" {
		t.Errorf("insert put the rune in the wrong place: %q", got)
	}
	if !e.Dirty() {
		t.Error("editing did not mark the buffer dirty")
	}
	e.Backspace()
	if got := e.Lines()[0]; got != "scripts {" {
		t.Errorf("backspace did not undo the insert: %q", got)
	}
}

func TestConfigEditor_BackspaceAtColumnZeroJoinsLines(t *testing.T) {
	e, _ := editorOn(t, "a\nb\n")
	e.MoveCursor(1, 0)
	e.MoveToLineStart()
	e.Backspace()
	if got := e.Lines()[0]; got != "ab" {
		t.Errorf("lines not joined: %q", got)
	}
	line, col := e.Cursor()
	if line != 0 || col != 1 {
		t.Errorf("cursor after join = (%d,%d), want (0,1)", line, col)
	}
}

func TestConfigEditor_BackspaceAtBufferStartIsANoop(t *testing.T) {
	// The cursor is already at the top-left; a join here would have to read a
	// line that does not exist.
	e, _ := editorOn(t, "a\nb\n")
	e.Backspace()
	if len(e.Lines()) != 3 || e.Lines()[0] != "a" {
		t.Errorf("buffer changed at start-of-file backspace: %q", e.Lines())
	}
}

func TestConfigEditor_NewLineCarriesIndentation(t *testing.T) {
	// Every block in this file is indented; starting each new line at column
	// zero would mean re-indenting by hand on every Enter.
	e, _ := editorOn(t, "scripts {\n    dev {\n    }\n}\n")
	e.MoveCursor(1, 0)
	e.MoveToLineEnd()
	e.NewLine()
	if got := e.Lines()[2]; got != "    " {
		t.Errorf("new line did not inherit indentation: %q", got)
	}
	_, col := e.Cursor()
	if col != 4 {
		t.Errorf("cursor should sit after the inherited indent, got col %d", col)
	}
}

func TestConfigEditor_DeleteJoinsFollowingLine(t *testing.T) {
	e, _ := editorOn(t, "a\nb\n")
	e.MoveToLineEnd()
	e.Delete()
	if got := e.Lines()[0]; got != "ab" {
		t.Errorf("delete at end of line did not join: %q", got)
	}
}

func TestConfigEditor_CursorStaysInsideTheBuffer(t *testing.T) {
	// A cursor that can leave the buffer turns the next insert into a panic in
	// the middle of someone's session, so every move is clamped.
	e, _ := editorOn(t, "one\ntwo\n")
	e.MoveCursor(-50, -50)
	if l, c := e.Cursor(); l != 0 || c != 0 {
		t.Errorf("cursor above the buffer = (%d,%d)", l, c)
	}
	e.MoveCursor(50, 50)
	l, c := e.Cursor()
	if l != len(e.Lines())-1 {
		t.Errorf("cursor below the buffer = line %d of %d", l, len(e.Lines()))
	}
	if c > len([]rune(e.Lines()[l])) {
		t.Errorf("cursor past end of line: col %d of %q", c, e.Lines()[l])
	}
	e.Insert("x") // must not panic
}

func TestConfigEditor_CursorIsRuneBased(t *testing.T) {
	// Byte offsets would let the cursor land inside a multi-byte character and
	// split it on the next edit.
	e, _ := editorOn(t, "// héllo wörld\n")
	e.MoveToLineEnd()
	_, col := e.Cursor()
	if col != len([]rune("// héllo wörld")) {
		t.Errorf("end-of-line column %d is byte-based", col)
	}
	e.Backspace()
	if got := e.Lines()[0]; got != "// héllo wörl" {
		t.Errorf("backspace split a multi-byte rune: %q", got)
	}
}

func TestConfigEditor_ReportsParseErrorsWhileTyping(t *testing.T) {
	// The footer has to answer "would this save?" before the developer reaches
	// for the save key.
	e, _ := editorOn(t, "scripts {\n}\n")
	if e.ParseError() != nil {
		t.Fatalf("valid config reported as broken: %v", e.ParseError())
	}
	e.MoveToLineEnd()
	e.Insert(" {")
	if e.ParseError() == nil {
		t.Error("unbalanced braces not reported")
	}
	e.Backspace()
	e.Backspace()
	if e.ParseError() != nil {
		t.Errorf("error not cleared after the edit was undone: %v", e.ParseError())
	}
}

// TestConfigEditor_SaveRefusesBrokenConfig is the load-bearing one: writing a
// config that does not parse takes out every autostart script and proxy on the
// next reload, so the file must be left exactly as it was.
func TestConfigEditor_SaveRefusesBrokenConfig(t *testing.T) {
	original := "scripts {\n    dev {\n        run \"npm run dev\"\n    }\n}\n"
	e, path := editorOn(t, original)

	e.MoveToLineEnd()
	e.Insert(" {") // unbalanced

	if err := e.Save(); err == nil {
		t.Fatal("a config that does not parse was written")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != original {
		t.Errorf("refused save still touched the file:\n%s", body)
	}
	if !e.Dirty() {
		t.Error("a refused save must leave the buffer dirty")
	}
	if !strings.Contains(e.Status(), "does not parse") {
		t.Errorf("status should carry the parse failure: %q", e.Status())
	}
}

func TestConfigEditor_SaveWritesAndClearsDirty(t *testing.T) {
	e, path := editorOn(t, "scripts {\n}\n")
	e.MoveCursor(1, 0)
	e.MoveToLineStart()
	e.Insert("    dev {\n    run \"npm run dev\"\n}\n")

	if err := e.Save(); err != nil {
		t.Fatalf("Save: %v\n%s", err, e.Text())
	}
	if e.Dirty() {
		t.Error("buffer still dirty after a successful save")
	}
	cfg, err := config.LoadAgntConfigFile(path)
	if err != nil {
		t.Fatalf("saved file does not load: %v", err)
	}
	if cfg.Scripts["dev"] == nil {
		t.Errorf("edit not persisted:\n%s", e.Text())
	}
}

func TestConfigEditor_EnsureVisibleTracksTheCursor(t *testing.T) {
	e, _ := editorOn(t, strings.Repeat("// line\n", 60))
	e.MoveCursor(50, 0)
	e.EnsureVisible(10)
	if s := e.Scroll(); s > 50 || s+10 <= 50 {
		t.Errorf("cursor line 50 not visible with scroll %d and height 10", s)
	}
	e.MoveCursor(-50, 0)
	e.EnsureVisible(10)
	if e.Scroll() != 0 {
		t.Errorf("scroll did not follow the cursor back to the top: %d", e.Scroll())
	}
}

func TestConfigEditor_CRLFIsNormalized(t *testing.T) {
	// A config edited on Windows would otherwise show a stray \r at every line
	// end and carry it into the saved file.
	e, _ := editorOn(t, "scripts {\r\n}\r\n")
	for i, line := range e.Lines() {
		if strings.Contains(line, "\r") {
			t.Errorf("line %d kept a carriage return: %q", i, line)
		}
	}
}
