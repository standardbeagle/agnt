package overlay

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/standardbeagle/vt10x"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSGRMouse(t *testing.T) {
	cases := []struct {
		seq  string
		want mouseEvent
		ok   bool
	}{
		{"\x1b[<0;12;3M", mouseEvent{button: mouseLeft, col: 12, row: 3}, true},
		{"\x1b[<0;12;3m", mouseEvent{button: mouseLeft, col: 12, row: 3, release: true}, true},
		{"\x1b[<32;5;5M", mouseEvent{button: mouseLeft, col: 5, row: 5, motion: true}, true},
		{"\x1b[<35;80;24M", mouseEvent{button: 3, col: 80, row: 24, motion: true}, true},
		{"\x1b[<64;1;1M", mouseEvent{button: mouseWheelUp, col: 1, row: 1}, true},
		{"\x1b[<65;1;1M", mouseEvent{button: mouseWheelDown, col: 1, row: 1}, true},
		{"\x1b[<20;7;9M", mouseEvent{button: mouseLeft, col: 7, row: 9}, true}, // ctrl+shift click is a click
		{"\x1b[<0;0;3M", mouseEvent{}, false},                                  // coordinates are 1-based
		{"\x1b[<0;12M", mouseEvent{}, false},
		{"\x1b[<0;12;3;4M", mouseEvent{}, false},
		{"\x1b[<a;12;3M", mouseEvent{}, false},
		{"\x1b[<0;99999999;3M", mouseEvent{}, false},
		{"\x1b[<0;12;3~", mouseEvent{}, false},
		{"\x1b[0;12;3M", mouseEvent{}, false},
		{"Up", mouseEvent{}, false},
	}
	for _, c := range cases {
		got, ok := parseSGRMouse(c.seq)
		assert.Equal(t, c.ok, ok, "%q", c.seq)
		assert.Equal(t, c.want, got, "%q", c.seq)
	}
}

// Closing the panels turns off what they turned on and puts back exactly the
// tracking mode and encoding the agent had.
func TestMouseRestoreSequencePutsBackTheAgentsModes(t *testing.T) {
	cases := map[vt10x.ModeFlag]string{
		0:                     "\x1b[?1000l\x1b[?1006l",
		vt10x.ModeMouseButton: "\x1b[?1000l\x1b[?1006l\x1b[?1000h",
		vt10x.ModeMouseMotion | vt10x.ModeMouseSgr: "\x1b[?1000l\x1b[?1006l\x1b[?1002h\x1b[?1006h",
		vt10x.ModeMouseMany | vt10x.ModeMouseSgr:   "\x1b[?1000l\x1b[?1006l\x1b[?1003h\x1b[?1006h",
		vt10x.ModeMouseX10:                         "\x1b[?1000l\x1b[?1006l\x1b[?9h",
		vt10x.ModeMouseSgr:                         "\x1b[?1000l\x1b[?1006l\x1b[?1006h",
	}
	for mode, want := range cases {
		assert.Equal(t, want, mouseRestoreSequence(mode), "mode %b", mode)
	}
}

// The filter reads the agent's mouse modes off the bytes it sends.
func TestFilterTracksTheChildsMouseModes(t *testing.T) {
	pw := NewProtectedWriter(&bytes.Buffer{}, 80, 24, FilterConfig{ProtectBottomRows: 1})
	t.Cleanup(pw.Stop)
	assert.Equal(t, "\x1b[?1000l\x1b[?1006l", pw.ChildMouseRestore(), "no mouse mode yet")
	_, _ = pw.Write([]byte("\x1b[?1003h\x1b[?1006h"))
	assert.Equal(t, "\x1b[?1000l\x1b[?1006l\x1b[?1003h\x1b[?1006h", pw.ChildMouseRestore())
	_, _ = pw.Write([]byte("\x1b[?1003l\x1b[?1006l"))
	assert.Equal(t, "\x1b[?1000l\x1b[?1006l", pw.ChildMouseRestore(), "turned off again")
}

// The panels borrow the mouse only when they can hand it back.
func TestPanelsBorrowAndReturnTheMouse(t *testing.T) {
	out := &safeWriter{} // closing starts a spinner that paints from its own goroutine
	o := New(nil, 80, 24, DefaultConfig())
	o.renderer.SetOutput(out)
	o.SetChildMouseRestore(func() string { return "<restore>" })

	o.mu.Lock()
	o.showPanelDirect()
	o.mu.Unlock()
	assert.Contains(t, out.String(), panelMouseOn, "opening the panels turns on click reports")
	assert.True(t, o.mouseBorrowed)

	o.mu.Lock()
	o.hideMenu()
	o.hideMenu()
	o.mu.Unlock()
	assert.Equal(t, 1, strings.Count(out.String(), "<restore>"), "closing hands the mouse back, once")
	assert.Less(t, strings.Index(out.String(), panelMouseOn), strings.Index(out.String(), "<restore>"))
	assert.False(t, o.mouseBorrowed)

	plain := &safeWriter{}
	o2 := New(nil, 80, 24, DefaultConfig())
	o2.renderer.SetOutput(plain)
	o2.mu.Lock()
	o2.showPanelDirect()
	o2.hideMenu()
	o2.mu.Unlock()
	assert.NotContains(t, plain.String(), panelMouseOn, "no way to restore, so the mouse is left alone")
}

// mouseScreen is a panel view drawn into a terminal model, so a test can
// click on what is on screen.
type mouseScreen struct {
	t      *testing.T
	o      *Overlay
	router *InputRouter
	term   vt10x.Terminal
}

func newMouseScreen(t *testing.T, panels []PanelItem, scripts ...string) *mouseScreen {
	o, router := newNavOverlay(t, panels, scripts...)
	term := vt10x.New(vt10x.WithSize(100, 30))
	o.renderer.SetOutput(term)
	o.status.DaemonConnected = ConnectionConnected
	s := &mouseScreen{t: t, o: o, router: router, term: term}
	o.Redraw()
	return s
}

func (s *mouseScreen) row(r int) string {
	var b strings.Builder
	for x := 0; x < 100; x++ {
		b.WriteRune(s.term.Cell(x, r-1).Char)
	}
	return b.String()
}

// find returns the 1-based column of text on row r.
func (s *mouseScreen) find(r int, text string) int {
	s.t.Helper()
	line := []rune(s.row(r))
	idx := strings.Index(string(line), text)
	require.GreaterOrEqual(s.t, idx, 0, "%q not on row %d: %q", text, r, string(line))
	return len([]rune(string(line)[:idx])) + 1
}

// findRow returns the first panel-body row (below the tab bar) holding text.
func (s *mouseScreen) findRow(text string) int {
	s.t.Helper()
	for r := 3; r <= 30; r++ {
		if strings.Contains(s.row(r), text) {
			return r
		}
	}
	s.t.Fatalf("%q not on screen", text)
	return 0
}

func (s *mouseScreen) send(button, row, col int, final byte) {
	s.router.handleMenuKey(fmt.Sprintf("\x1b[<%d;%d;%d%c", button, col, row, final))
}

func (s *mouseScreen) click(row, col int) {
	s.send(mouseLeft, row, col, 'M')
	s.send(mouseLeft, row, col, 'm')
}

func TestClickingTabsSwitchesAndClosesPanels(t *testing.T) {
	s := newMouseScreen(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "web", Label: "web", ProcessState: "running"},
		{Type: "process", ID: "old", Label: "old", ProcessState: "failed"},
		{Type: "log", ID: "__log", Label: "log"},
	}, "web")

	tabs := s.row(1)
	assert.Contains(t, tabs, "● web", "a running process tab shows its state")
	assert.Contains(t, tabs, "✗ old ×", "a failed one shows its state and a close button")

	s.click(1, s.find(1, "web"))
	assert.Equal(t, 1, s.o.panelIndex, "clicking a tab opens it")
	s.click(1, s.find(1, "log"))
	assert.Equal(t, 3, s.o.panelIndex)
	s.click(1, s.find(1, "overview"))
	assert.Equal(t, 0, s.o.panelIndex, "the overview tab is a tab too")

	s.click(1, s.find(1, "×"))
	require.Len(t, s.o.panelItems, 3, "× closes the finished panel")
	for _, p := range s.o.panelItems {
		assert.NotEqual(t, "old", p.ID)
	}
	assert.Equal(t, StateMenu, s.o.State())
}

func TestClickingFooterHintsPressesTheirKeys(t *testing.T) {
	s := newMouseScreen(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "web", Label: "web", ProcessState: "stopped"},
	}, "web")
	footer := 29
	s.click(footer, s.find(footer, "Tab Panels"))
	assert.Equal(t, 1, s.o.panelIndex, "Tab Panels clicked moves to the next panel")

	s.click(footer, s.find(footer, "Esc Overview")+1)
	assert.Equal(t, 0, s.o.panelIndex, "Esc Overview clicked goes back")

	s.click(footer, s.find(footer, ": Cmds"))
	assert.True(t, s.o.commandInput, ": Cmds clicked opens the palette")
	s.click(1, s.find(1, "web"))
	assert.False(t, s.o.commandInput, "clicking away to a tab closes the palette")
	assert.Equal(t, 1, s.o.panelIndex)

	s.click(s.findRow("x close"), s.find(s.findRow("x close"), "x close"))
	assert.Len(t, s.o.panelItems, 1, "the header's x close button closes the finished panel")

	s.click(footer, s.find(footer, "Esc Close"))
	assert.NotEqual(t, StateMenu, s.o.State(), "Esc Close clicked closes the view")
}

func TestClickingScriptRowsSelectsThenOpens(t *testing.T) {
	s := newMouseScreen(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "web", Label: "web"},
		{Type: "process", ID: "api", Label: "api"},
	}, "web", "api")

	assert.Contains(t, s.row(s.findRow("web")), strings.TrimSpace(openTag), "the highlighted script says Enter opens it")

	api := s.findRow("api")
	s.click(api, s.find(api, "api"))
	assert.Equal(t, 1, s.o.overviewSelectedIdx, "first click selects")
	assert.Equal(t, 0, s.o.panelIndex)
	assert.Contains(t, s.row(api), strings.TrimSpace(openTag), "the hint follows the highlight")

	s.click(api, s.find(api, "api"))
	assert.Equal(t, 2, s.o.panelIndex, "second click on the selected script opens it")
}

func TestWheelScrollsTextAndStepsLists(t *testing.T) {
	log := PanelItem{Type: "log", ID: "__log", Label: "log"}
	log.SetContent(strings.Repeat("line\n", 200))
	s := newMouseScreen(t, []PanelItem{{Type: "overview", Label: "overview"}, log}, "web", "api", "db")

	s.send(mouseWheelDown, 10, 10, 'M')
	assert.Equal(t, 1, s.o.overviewSelectedIdx, "one notch steps the script list by one")

	s.click(1, s.find(1, "log"))
	s.send(mouseWheelUp, 10, 10, 'M')
	s.send(mouseWheelUp|8, 10, 10, 'M') // with Alt held: still the wheel
	assert.Equal(t, 2*wheelLines, s.o.panelItems[1].ScrollOffset, "each notch scrolls three lines")

	s.o.Redraw()
	r := s.findRow("newer · End")
	s.click(r, s.find(r, "newer"))
	assert.Equal(t, 0, s.o.panelItems[1].ScrollOffset, "the ↓ marker jumps to the live tail")

	s.o.Redraw()
	r = s.findRow(" more ")
	s.click(r, s.find(r, "more"))
	assert.Positive(t, s.o.panelItems[1].ScrollOffset, "the ↑ marker pages up")
}

// Releases and motion carry no action and must not disturb key state: a
// click's release lands between the two presses of Ctrl+C twice.
func TestMouseReleaseAndMotionChangeNothing(t *testing.T) {
	s := newMouseScreen(t, []PanelItem{{Type: "overview", Label: "overview"}, {Type: "log", Label: "log"}})
	s.o.renderer.SetOutput(&safeWriter{}) // the armed notice paints from its own goroutine
	quits := 0
	s.router.SetQuitHandler(func() { quits++ })

	s.router.handleMenuKey("\x03")
	s.send(mouseLeft, 1, 5, 'm')
	s.send(mouseLeft|mouseMotionBit, 1, 5, 'M')
	s.send(2, 1, 5, 'M') // right button: nothing bound
	assert.True(t, s.router.ctrlCExitArmed, "release, motion and unbound buttons leave Ctrl+C armed")
	s.router.handleMenuKey("\x03")
	assert.Equal(t, 1, quits)

	s2 := newMouseScreen(t, []PanelItem{{Type: "overview", Label: "overview"}, {Type: "log", Label: "log"}})
	s2.click(15, 50) // empty space
	assert.Equal(t, 0, s2.o.panelIndex)
	assert.Equal(t, StateMenu, s2.o.State())
}

// Tab widths with state glyphs and close buttons still fit the window, and
// every visible tab is clickable where it is drawn.
func TestTabHitsMatchWhereTabsAreDrawn(t *testing.T) {
	var panels []PanelItem
	panels = append(panels, PanelItem{Type: "overview", Label: "overview"})
	states := []string{"running", "failed", "stopped", ""}
	for i := range 16 {
		panels = append(panels, PanelItem{Type: "process", ID: fmt.Sprintf("s%02d", i), Label: fmt.Sprintf("s%02d", i), ProcessState: states[i%4]})
	}
	s := newMouseScreen(t, panels)
	for _, active := range []int{0, 8, 16} {
		s.o.mu.Lock()
		s.o.panelIndex = active
		s.o.mu.Unlock()
		s.o.Redraw()
		tabs := s.row(1)
		assert.LessOrEqual(t, len([]rune(strings.TrimRight(tabs, " "))), 100)
		first, last := tabWindow(panels, active, 100)
		for i := first; i <= last; i++ {
			col := s.find(1, panels[i].Label)
			hit, ok := s.o.renderer.PanelHitAt(1, col)
			require.True(t, ok, "tab %d", i)
			assert.Equal(t, hitTab, hit.Kind)
			assert.Equal(t, i, hit.Index, "tab %d label is its own hit", i)
		}
		if first > 0 {
			hit, ok := s.o.renderer.PanelHitAt(1, 1)
			require.True(t, ok, "‹ is clickable")
			assert.Equal(t, first-1, hit.Index)
		}
		if last < len(panels)-1 {
			col := s.find(1, "›")
			hit, ok := s.o.renderer.PanelHitAt(1, col)
			require.True(t, ok, "› is clickable")
			assert.Equal(t, last+1, hit.Index)
		}
	}
}
