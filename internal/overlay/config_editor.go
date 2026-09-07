package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
)

// ConfigEditor is the editable .agnt.kdl buffer behind the overview's `:config`
// command. It is deliberately a plain model — no terminal, no daemon — so the
// editing rules can be tested directly; the renderer and the input router are
// thin wrappers over it.
//
// The buffer is a []string of lines with a cursor. Every mutation keeps the
// cursor inside the text, because a cursor that can leave the buffer turns a
// later insert into a panic in the middle of someone's session.
type ConfigEditor struct {
	path  string
	lines []string

	// cursorLine/cursorCol are measured in RUNES, not bytes: a config with a
	// non-ASCII string value would otherwise let the cursor land inside a
	// character and split it on the next edit.
	cursorLine int
	cursorCol  int

	scroll int
	dirty  bool

	// status is the last save/validation message shown in the panel footer.
	status string
	// invalid holds the current parse error, or nil while the buffer parses.
	// Recomputed on every mutation so the footer answers "would this save?"
	// before the developer reaches for the save key.
	invalid error
	// existed records whether the file was on disk when the editor opened, so
	// the footer can say the save will create it.
	existed bool
	// original is the exact disk content, before newline normalization.
	original string
}

// NewConfigEditor loads path for editing. A project with no .agnt.kdl opens on
// the documented default template rather than an empty buffer — an empty buffer
// says nothing about which keys exist.
func NewConfigEditor(path string) (*ConfigEditor, error) {
	e := &ConfigEditor{path: path}

	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		e.existed = true
		e.original = string(body)
		e.lines = strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	case os.IsNotExist(err):
		e.lines = strings.Split(config.DefaultAgntConfigKDL(), "\n")
		e.status = "new file — save writes " + path
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(e.lines) == 0 {
		e.lines = []string{""}
	}
	e.revalidate()
	return e, nil
}

// Text returns the buffer as it would be written.
func (e *ConfigEditor) Text() string { return strings.Join(e.lines, "\n") }

// Lines returns the buffer's lines for rendering.
func (e *ConfigEditor) Lines() []string { return e.lines }

// Cursor returns the cursor position in runes.
func (e *ConfigEditor) Cursor() (line, col int) { return e.cursorLine, e.cursorCol }

// Dirty reports unsaved changes.
func (e *ConfigEditor) Dirty() bool { return e.dirty }

// Path is the file being edited.
func (e *ConfigEditor) Path() string { return e.path }

// Status is the message for the panel footer.
func (e *ConfigEditor) Status() string { return e.status }

// Scroll is the first visible line index.
func (e *ConfigEditor) Scroll() int { return e.scroll }

// ParseError returns the current parse failure, or nil when the buffer would
// save cleanly.
func (e *ConfigEditor) ParseError() error { return e.invalid }

func (e *ConfigEditor) runes(line int) []rune { return []rune(e.lines[line]) }

func (e *ConfigEditor) revalidate() {
	e.invalid = config.ValidateAgntConfigText(e.Text())
}

// clampCursor keeps the cursor on a real line and inside that line.
func (e *ConfigEditor) clampCursor() {
	if e.cursorLine < 0 {
		e.cursorLine = 0
	}
	if e.cursorLine >= len(e.lines) {
		e.cursorLine = len(e.lines) - 1
	}
	if e.cursorCol < 0 {
		e.cursorCol = 0
	}
	if n := len(e.runes(e.cursorLine)); e.cursorCol > n {
		e.cursorCol = n
	}
}

// Insert types text at the cursor. Tabs become four spaces to match the
// indentation the config writer and the shipped template use.
func (e *ConfigEditor) Insert(text string) {
	if text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\t", "    ")
	for _, r := range text {
		if r == '\n' {
			e.NewLine()
			continue
		}
		line := e.runes(e.cursorLine)
		line = append(line[:e.cursorCol], append([]rune{r}, line[e.cursorCol:]...)...)
		e.lines[e.cursorLine] = string(line)
		e.cursorCol++
	}
	e.markEdited()
}

// NewLine splits the current line at the cursor, carrying the current line's
// indentation onto the new one — every block in this file is indented, so
// starting each line at column zero would mean re-indenting by hand constantly.
func (e *ConfigEditor) NewLine() {
	line := e.runes(e.cursorLine)
	head := string(line[:e.cursorCol])
	tail := string(line[e.cursorCol:])
	indent := head[:len(head)-len(strings.TrimLeft(head, " "))]

	e.lines[e.cursorLine] = head
	rest := append([]string{indent + tail}, e.lines[e.cursorLine+1:]...)
	e.lines = append(e.lines[:e.cursorLine+1], rest...)
	e.cursorLine++
	e.cursorCol = len([]rune(indent))
	e.markEdited()
}

// Backspace deletes the rune before the cursor, joining lines at column 0.
func (e *ConfigEditor) Backspace() {
	if e.cursorCol > 0 {
		line := e.runes(e.cursorLine)
		e.lines[e.cursorLine] = string(append(line[:e.cursorCol-1], line[e.cursorCol:]...))
		e.cursorCol--
		e.markEdited()
		return
	}
	if e.cursorLine == 0 {
		return // start of buffer: nothing to join
	}
	prev := e.runes(e.cursorLine - 1)
	e.cursorCol = len(prev)
	e.lines[e.cursorLine-1] = string(prev) + e.lines[e.cursorLine]
	e.lines = append(e.lines[:e.cursorLine], e.lines[e.cursorLine+1:]...)
	e.cursorLine--
	e.markEdited()
}

// Delete removes the rune under the cursor, joining the next line at end of line.
func (e *ConfigEditor) Delete() {
	line := e.runes(e.cursorLine)
	if e.cursorCol < len(line) {
		e.lines[e.cursorLine] = string(append(line[:e.cursorCol], line[e.cursorCol+1:]...))
		e.markEdited()
		return
	}
	if e.cursorLine >= len(e.lines)-1 {
		return // end of buffer
	}
	e.lines[e.cursorLine] = string(line) + e.lines[e.cursorLine+1]
	e.lines = append(e.lines[:e.cursorLine+1], e.lines[e.cursorLine+2:]...)
	e.markEdited()
}

func (e *ConfigEditor) markEdited() {
	e.dirty = true
	e.status = ""
	e.revalidate()
}

// MoveCursor moves by dLine/dCol, clamped to the buffer.
func (e *ConfigEditor) MoveCursor(dLine, dCol int) {
	e.cursorLine += dLine
	e.cursorCol += dCol
	if dLine != 0 {
		// Vertical movement lands at the same column when the target line is
		// long enough, and at its end when it is not.
		e.clampCursor()
		return
	}
	// Horizontal movement wraps across line ends so holding an arrow key walks
	// the document rather than sticking at a boundary.
	if e.cursorCol < 0 && e.cursorLine > 0 {
		e.cursorLine--
		e.cursorCol = len(e.runes(e.cursorLine))
	}
	if e.cursorLine < len(e.lines) && e.cursorCol > len(e.runes(e.cursorLine)) && e.cursorLine < len(e.lines)-1 {
		e.cursorLine++
		e.cursorCol = 0
	}
	e.clampCursor()
}

// MoveToLineStart / MoveToLineEnd implement Home and End.
func (e *ConfigEditor) MoveToLineStart() { e.cursorCol = 0 }
func (e *ConfigEditor) MoveToLineEnd()   { e.cursorCol = len(e.runes(e.cursorLine)) }

// EnsureVisible scrolls so the cursor is inside a viewport of height lines.
func (e *ConfigEditor) EnsureVisible(height int) {
	if height <= 0 {
		return
	}
	if e.cursorLine < e.scroll {
		e.scroll = e.cursorLine
	}
	if e.cursorLine >= e.scroll+height {
		e.scroll = e.cursorLine - height + 1
	}
	if e.scroll < 0 {
		e.scroll = 0
	}
}

// Save validates and writes the buffer. A buffer that would not parse is
// refused with the parse error, leaving the file untouched: writing it would
// take out every autostart script and proxy on the next reload.
func (e *ConfigEditor) Save() error {
	body, err := os.ReadFile(e.path)
	if err != nil && !os.IsNotExist(err) {
		e.status = fmt.Sprintf("not saved: read config: %v", err)
		return fmt.Errorf("%s", e.status)
	}
	if (err == nil) != e.existed || (err == nil && string(body) != e.original) {
		e.status = "not saved: config changed on disk; reopen the editor before saving"
		return fmt.Errorf("%s", e.status)
	}
	if err := config.SaveAgntConfigText(e.path, e.Text()); err != nil {
		e.status = err.Error()
		return err
	}
	e.dirty = false
	e.existed = true
	e.original = e.Text()
	e.status = "saved " + e.path
	return nil
}

// openConfigPanel loads .agnt.kdl into the editor and focuses its panel,
// creating the panel when this is the first time it is opened. Must be called
// with the overlay lock held.
func (r *InputRouter) openConfigPanel() error {
	projectPath := r.scriptController.ProjectPath()
	if projectPath == "" {
		return fmt.Errorf("no project directory to find .agnt.kdl in")
	}
	path := filepath.Join(projectPath, config.AgntConfigFileName)

	if r.overlay.configEditor == nil || r.overlay.configEditor.Path() != path {
		// Reopening an unsaved buffer would silently discard the edits, so a
		// dirty editor for the same file is kept as it is.
		editor, err := NewConfigEditor(path)
		if err != nil {
			return err
		}
		r.overlay.configEditor = editor
	}

	for i, p := range r.overlay.panelItems {
		if p.Type == "config" {
			r.overlay.panelIndex = i
			return nil
		}
	}
	r.overlay.panelItems = append(r.overlay.panelItems, PanelItem{
		Type: "config", ID: "__config", Label: "config",
	})
	r.overlay.panelIndex = len(r.overlay.panelItems) - 1
	return nil
}

// closeConfigPanel drops the panel and its buffer. A dirty buffer is refused
// once: losing typed config to a stray Escape would be a poor trade, so the
// first press reports the unsaved state and the second one discards.
func (r *InputRouter) closeConfigPanel() {
	if e := r.overlay.configEditor; e != nil && e.Dirty() && !r.configDiscardArmed {
		r.configDiscardArmed = true
		r.overlay.Notify(Notification{
			Level: LevelWarn,
			Text:  "unsaved config changes — ^S to save, Esc again to discard",
		})
		return
	}
	r.configDiscardArmed = false
	r.overlay.configEditor = nil
	for i, p := range r.overlay.panelItems {
		if p.Type == "config" {
			r.overlay.panelItems = append(r.overlay.panelItems[:i], r.overlay.panelItems[i+1:]...)
			if r.overlay.panelIndex >= len(r.overlay.panelItems) {
				r.overlay.panelIndex = len(r.overlay.panelItems) - 1
			}
			break
		}
	}
}

// handleConfigKey routes one parsed key into the editor. It returns false for
// keys the editor does not consume, so panel navigation still works.
//
// Everything printable is consumed: a config panel that let 'q' or 'x' fall
// through to the panel browser would close itself the moment someone typed a
// word containing them.
func (r *InputRouter) handleConfigKey(key string) bool {
	e := r.overlay.configEditor
	if e == nil {
		return false
	}
	// Any key other than a second Escape re-arms the discard guard, so the
	// confirmation cannot be satisfied by an Escape pressed minutes earlier.
	if !strings.HasPrefix(key, "Escape") {
		r.configDiscardArmed = false
	}
	// "Escape+x" is the reader's spelling for Escape followed quickly by
	// another key; for this panel it means the same thing as Escape.
	if strings.HasPrefix(key, "Escape+") {
		key = "Escape"
	}

	switch key {
	case "Escape":
		r.closeConfigPanel()
	// Raw control bytes, not names: the escape reader only names the keys that
	// arrive as CSI sequences, so Ctrl+S and Backspace reach us as 0x13 and
	// 0x7f. Both spellings are accepted so a future named-key change here
	// cannot silently unbind saving.
	case "\x13", "Ctrl+S":
		if err := e.Save(); err != nil {
			r.overlay.Notify(Notification{Level: LevelError, Text: err.Error()})
		} else {
			r.overlay.Notify(Notification{Level: LevelInfo, Text: e.Status()})
			// A saved config is only useful once the daemon has it.
			if err := r.scriptController.ReconcileConfig(); err != nil {
				r.overlay.Notify(Notification{
					Level: LevelWarn,
					Text:  "saved, but could not apply live: " + err.Error(),
				})
			}
		}
	case "Up":
		e.MoveCursor(-1, 0)
	case "Down":
		e.MoveCursor(1, 0)
	case "Left":
		e.MoveCursor(0, -1)
	case "Right":
		e.MoveCursor(0, 1)
	case "Home":
		e.MoveToLineStart()
	case "End":
		e.MoveToLineEnd()
	case "\x7f", "\b", "Backspace":
		e.Backspace()
	case "Delete":
		e.Delete()
	case "\r", "\n":
		e.NewLine()
	case "\t":
		e.Insert("\t")
	default:
		if len(key) >= 1 && !strings.HasPrefix(key, "Ctrl+") && !strings.HasPrefix(key, "Escape+") {
			// Parsed keys are either a named key handled above or the literal
			// text typed; anything with a control byte is not text.
			if !strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 }) {
				e.Insert(key)
				break
			}
		}
		return false
	}
	r.overlay.draw()
	return true
}
