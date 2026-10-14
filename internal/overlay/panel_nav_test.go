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

// newNavOverlay returns an overlay in panel view on the overview, with the
// given panels and scripts, drawing into a discarded buffer.
func newNavOverlay(t *testing.T, panels []PanelItem, scripts ...string) (*Overlay, *InputRouter) {
	t.Helper()
	o := New(nil, 100, 30, DefaultConfig())
	o.renderer.SetOutput(&bytes.Buffer{})
	o.state.Store(int32(StateMenu))
	o.panelMode = true
	o.panelItems = panels
	for _, s := range scripts {
		o.status.Scripts = append(o.status.Scripts, ScriptInfo{Name: s, State: "running"})
	}
	return o, NewInputRouter(nil, o)
}

func TestEscapeReturnsToOverviewBeforeClosing(t *testing.T) {
	o, router := newNavOverlay(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "web", Label: "web"},
		{Type: "log", ID: "__log", Label: "log"},
	}, "web")
	o.panelIndex = 2

	router.handleMenuKey("Escape")
	assert.Equal(t, StateMenu, o.State(), "Escape on a panel must not close the view")
	assert.Equal(t, 0, o.panelIndex, "Escape on a panel returns to the overview")

	router.handleMenuKey("Escape")
	assert.NotEqual(t, StateMenu, o.State(), "Escape on the overview closes the view")
}

func TestQuitClosesFromAnyPanel(t *testing.T) {
	o, router := newNavOverlay(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "log", ID: "__log", Label: "log"},
	})
	o.panelIndex = 1
	router.handleMenuKey("q")
	assert.NotEqual(t, StateMenu, o.State())
}

func TestPlainArrowsMoveBetweenPanels(t *testing.T) {
	o, router := newNavOverlay(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "proxy", ID: "p", Label: "p"},
		{Type: "log", ID: "__log", Label: "log"},
	})
	router.handleMenuKey("Right")
	assert.Equal(t, 1, o.panelIndex, "→ moves to the next panel, mirroring ←")
	router.handleMenuKey("Right")
	assert.Equal(t, 2, o.panelIndex)
	router.handleMenuKey("Left")
	assert.Equal(t, 1, o.panelIndex)
}

// The digit keys open scripts by the number the overview prints beside them,
// which follows the script list, not the order panels happened to be created.
func TestDigitOpensScriptNumberedOnOverview(t *testing.T) {
	o, router := newNavOverlay(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "api", Label: "api"},
		{Type: "process", ID: "web", Label: "web"},
	}, "web", "api")

	router.handleMenuKey("1")
	require.Equal(t, "web", o.panelItems[o.panelIndex].ID)
	assert.Equal(t, 0, o.overviewSelectedIdx, "the overview highlight follows the opened script")

	router.handleMenuKey("2")
	assert.Equal(t, "api", o.panelItems[o.panelIndex].ID)

	before := o.panelIndex
	router.handleMenuKey("9")
	assert.Equal(t, before, o.panelIndex, "a digit with no script does nothing")
}

func TestPageKeysScrollPanelContent(t *testing.T) {
	o, router := newNavOverlay(t, []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "log", ID: "__log", Label: "log"},
	})
	o.panelItems[1].SetContent(strings.Repeat("line\n", 200))
	o.panelIndex = 1

	router.handleMenuKey("PageUp")
	assert.Equal(t, router.pageLines(), o.panelItems[1].ScrollOffset)
	router.handleMenuKey("Home")
	assert.Equal(t, o.panelItems[1].ContentLines()-1, o.panelItems[1].ScrollOffset, "Home goes to the oldest line")
	router.handleMenuKey("End")
	assert.Equal(t, 0, o.panelItems[1].ScrollOffset, "End pins to the newest line")
	router.handleMenuKey("PageDown")
	assert.Equal(t, 0, o.panelItems[1].ScrollOffset, "scrolling never passes the bottom")
}

func TestPageKeyNamesParsed(t *testing.T) {
	for seq, want := range map[string]string{"\x1b[5~": "PageUp", "\x1b[6~": "PageDown", "\x1b[1~": "Home", "\x1b[4~": "End"} {
		r := NewEscapeSequenceReader()
		var got string
		for i := 0; i < len(seq); i++ {
			if k, done := r.Feed(seq[i]); done {
				got = k
			}
		}
		assert.Equal(t, want, got, "%q", seq)
	}
}

func TestConfigEditorDoesNotTypeKeyNames(t *testing.T) {
	o, router := newNavOverlay(t, []PanelItem{{Type: "overview", Label: "overview"}, {Type: "config", ID: "__config", Label: "config"}})
	o.configEditor = editorFor(t, "scripts {\n}\n")
	o.panelIndex = 1
	for _, key := range []string{"PageUp", "PageDown", "Shift+Tab"} {
		router.handleMenuKey(key)
	}
	assert.False(t, o.configEditor.Dirty(), "a named key was inserted into the buffer as text")
	assert.Equal(t, 1, o.panelIndex, "the editor keeps focus")
}

func TestPanelOrderIsStableAcrossLateArrivals(t *testing.T) {
	o, _ := newNavOverlay(t, nil, "web")
	o.buildPanelItems()
	// A script that starts after the first open, and the config editor
	// opened later, both used to land after the log panel.
	o.status.Scripts = append(o.status.Scripts, ScriptInfo{Name: "api", State: "running"})
	o.status.Proxies = []ProxyInfo{{ID: "dev"}}
	o.configEditor = editorFor(t, "")
	o.panelIndex = 1 // web
	o.buildPanelItems()

	var types []string
	for _, p := range o.panelItems {
		types = append(types, p.Type+":"+p.Label)
	}
	assert.Equal(t, []string{"overview:overview", "process:web", "process:api", "proxy:dev", "config:config", "log:log"}, types)
	assert.Equal(t, "web", o.panelItems[o.panelIndex].ID, "reordering keeps the active panel focused")
}

func TestOverviewScriptListFollowsSelection(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, 100, 40)
	var scripts []ScriptInfo
	for i := 0; i < 12; i++ {
		scripts = append(scripts, ScriptInfo{Name: fmt.Sprintf("script%02d", i), State: "running"})
	}
	status := Status{DaemonConnected: ConnectionConnected, Scripts: scripts}
	r.DrawPanelView([]PanelItem{{Type: "overview", Label: "overview"}}, 0, status, 11, false, "", 0, false, OverviewActions{})
	assert.Contains(t, out.String(), "script11", "the highlighted script must be on screen")
	assert.Contains(t, out.String(), "8 of 12 shown")
}

// A full overview used to push the palette below the panel, so typing went
// into a box that was not drawn.
func TestPaletteVisibleOnCrowdedOverview(t *testing.T) {
	const width, height = 80, 24
	term := vt10x.New(vt10x.WithSize(width, height))
	r := NewRenderer(term, width, height)
	var scripts []ScriptInfo
	var ports []PortInfo
	for i := 0; i < 8; i++ {
		scripts = append(scripts, ScriptInfo{Name: fmt.Sprintf("s%d", i), State: "running"})
		ports = append(ports, PortInfo{Port: 3000 + i, Name: "node", Status: "unmanaged"})
	}
	status := Status{DaemonConnected: ConnectionConnected, Scripts: scripts, Ports: ports}
	r.DrawPanelView([]PanelItem{{Type: "overview", Label: "overview"}}, 0, status, 0, true, "kill", 0, false, OverviewActions{})

	// The panel body ends above its bottom border, the footer and the status
	// bar; the palette has to be drawn inside it.
	body := ""
	for y := 0; y < height-3; y++ {
		for x := 0; x < width; x++ {
			body += string(term.Cell(x, y).Char)
		}
		body += "\n"
	}
	assert.Contains(t, body, "run command")
	assert.Contains(t, body, "> kill")
	assert.Contains(t, body, "kill-port")
}

func TestPanelFooterShowsLiveNotification(t *testing.T) {
	var out bytes.Buffer
	o, _ := newNavOverlay(t, []PanelItem{{Type: "overview", Label: "overview"}})
	o.renderer.SetOutput(&out)
	o.Notify(Notification{Level: LevelError, Text: "stop web failed: boom"})
	assert.Contains(t, out.String(), "stop web failed: boom",
		"a notification raised while a panel is open must be visible there")
}

func TestFooterHintsOnlyOfferKeysThatWork(t *testing.T) {
	running := PanelItem{Type: "process", ID: "web", ProcessState: "running"}
	stopped := PanelItem{Type: "process", ID: "web", ProcessState: "stopped"}
	assert.NotContains(t, panelHints(running, Status{}, OverviewActions{}), "x Close")
	assert.Contains(t, panelHints(stopped, Status{}, OverviewActions{}), "x Close")
	assert.Contains(t, panelHints(running, Status{}, OverviewActions{}), "Esc Overview")

	overview := PanelItem{Type: "overview"}
	assert.Contains(t, panelHints(overview, Status{DaemonConnected: ConnectionConnected}, OverviewActions{}), ": Cmds",
		"the palette works with no scripts, so its hint must show with none")
}

func TestTabWindowKeepsActiveVisible(t *testing.T) {
	var panels []PanelItem
	for i := 0; i < 20; i++ {
		panels = append(panels, PanelItem{Label: fmt.Sprintf("panel%02d", i)})
	}
	for _, active := range []int{0, 7, 19} {
		first, last := tabWindow(panels, active, 60)
		assert.LessOrEqual(t, first, active)
		assert.GreaterOrEqual(t, last, active)
		width := 3
		for i := first; i <= last; i++ {
			width += len(panels[i].Label) + 3
		}
		assert.LessOrEqual(t, width, 60, "active=%d window [%d,%d] overflows", active, first, last)
	}
	first, last := tabWindow(panels[:3], 1, 200)
	assert.Equal(t, [2]int{0, 2}, [2]int{first, last}, "everything fits: no window")
}

// A palette command's result (kill-orphans, a proxy stop) has to show on the
// open panel view when the status refresh lands, not on the next keypress.
func TestStatusUpdateRepaintsOpenPanelView(t *testing.T) {
	var out bytes.Buffer
	o, _ := newNavOverlay(t, []PanelItem{{Type: "overview", Label: "overview"}})
	o.renderer.SetOutput(&out)
	o.UpdateStatus(Status{DaemonConnected: ConnectionConnected, Orphans: []OrphanInfo{{PGID: 4242, Count: 2}}})
	require.Contains(t, out.String(), "4242")

	out.Reset()
	o.UpdateStatus(Status{DaemonConnected: ConnectionConnected})
	assert.NotEmpty(t, out.String(), "the reaped orphan must be repainted away")
	assert.NotContains(t, out.String(), "ORPHANS")
}

func TestStatusUpdateAddsAndRemovesProxyTabs(t *testing.T) {
	o, _ := newNavOverlay(t, []PanelItem{{Type: "overview", Label: "overview"}})
	o.buildPanelItems()

	o.UpdateStatus(Status{DaemonConnected: ConnectionConnected, Proxies: []ProxyInfo{{ID: "dev"}, {ID: "api"}}})
	var ids []string
	for _, p := range o.panelItems {
		if p.Type == "proxy" {
			ids = append(ids, p.ID)
		}
	}
	require.Equal(t, []string{"dev", "api"}, ids, "a started proxy gets a tab while the view is open")

	for i, p := range o.panelItems {
		if p.ID == "api" {
			o.panelIndex = i
		}
	}
	o.UpdateStatus(Status{DaemonConnected: ConnectionConnected, Proxies: []ProxyInfo{{ID: "api"}}})
	require.Equal(t, "api", o.panelItems[o.panelIndex].ID, "removing an earlier tab keeps the viewed one focused")

	o.UpdateStatus(Status{DaemonConnected: ConnectionDisconnected})
	require.Equal(t, "api", o.panelItems[o.panelIndex].ID, "a daemon blip must not drop proxy tabs")

	o.UpdateStatus(Status{DaemonConnected: ConnectionConnected})
	assert.Equal(t, 0, o.panelIndex, "the viewed proxy stopped: back to the overview")
	for _, p := range o.panelItems {
		assert.NotEqual(t, "proxy", p.Type)
	}
}
