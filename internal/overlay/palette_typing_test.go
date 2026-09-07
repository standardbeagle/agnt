package overlay

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/standardbeagle/vt10x"
)

func TestCommandPaletteTypingPreservesPanel(t *testing.T) {
	var output bytes.Buffer
	o := New(nil, 120, 40, DefaultConfig())
	o.renderer.SetOutput(&output)
	o.state.Store(int32(StateMenu))
	o.panelMode = true
	o.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
	router := NewInputRouter(nil, o)
	router.handleMenuKey(":")
	if !o.commandInput {
		t.Fatal("colon did not open the command palette")
	}
	for _, key := range []string{"s", "t", "a", "r", "t", "Backspace", "Down", "Up", "\t"} {
		output.Reset()
		router.handleMenuKey(key)
		frame := output.String()
		if strings.Contains(frame, ClearScreen) {
			t.Fatalf("key %q cleared the screen", key)
		}
		if strings.Contains(frame, "Ctrl+→ Panels") {
			t.Fatalf("key %q redrew the panel footer", key)
		}
	}
	router.handleMenuKey("Escape")
	if o.commandInput || o.State() != StateMenu {
		t.Fatal("escape must close the command palette and keep the panel open")
	}
}

func TestCommandPaletteRefreshMatchesFullDraw(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			const height = 40
			terminal := vt10x.New(vt10x.WithSize(width, height))
			r := NewRenderer(terminal, width, height)
			panels := []PanelItem{{Type: "overview", Label: "overview"}}
			status := Status{DaemonConnected: ConnectionConnected}
			r.DrawPanelView(panels, 0, status, 0, true, "", 0, false, OverviewActions{})
			for _, query := range []string{"s", "start", "start " + strings.Repeat("x", width*2), "zzz", "", "stop"} {
				r.DrawPanelView(panels, 0, status, 0, true, query, 0, false, OverviewActions{})
				fresh := vt10x.New(vt10x.WithSize(width, height))
				NewRenderer(fresh, width, height).DrawPanelView(panels, 0, status, 0, true, query, 0, false, OverviewActions{})
				for y := 0; y < height; y++ {
					for x := 0; x < width; x++ {
						got, want := terminal.Cell(x, y), fresh.Cell(x, y)
						if got != want {
							t.Fatalf("query %q cell(%d,%d): got %+v, want %+v", query, x, y, got, want)
						}
					}
				}
			}
		})
	}
}
