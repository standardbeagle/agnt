package overlay

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/standardbeagle/vt10x"
)

func TestPanelFramesMatchFreshView(t *testing.T) {
	for _, width := range []int{60, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			const height = 40
			terminal := vt10x.New(vt10x.WithSize(width, height))
			var output bytes.Buffer
			r := NewRenderer(io.MultiWriter(terminal, &output), width, height)
			panels := []PanelItem{{Type: "overview", Label: "overview"}, {Type: "log", Label: "log"}}
			status := Status{DaemonConnected: ConnectionConnected, Scripts: []ScriptInfo{
				{Name: "web", State: "running"}, {Name: "api", State: "failed", LastError: "connection failed"},
			}}
			for i, step := range []struct {
				panel, selected int
				content, query  string
				command         bool
			}{
				{}, {selected: 1}, {}, {command: true, query: "s"},
				{command: true, query: "start"}, {},
				{panel: 1, content: "first\nsecond\nthird"},
				{panel: 1, content: "first\nchanged\nthird"},
				{panel: 1, content: "first"}, {},
			} {
				panels[1].SetContent(step.content)
				output.Reset()
				r.DrawPanelView(panels, step.panel, status, step.selected, step.command, step.query, 0, false, OverviewActions{})
				if i > 0 && strings.Contains(output.String(), ClearScreen) {
					t.Fatalf("step %d cleared the screen", i)
				}
				fresh := vt10x.New(vt10x.WithSize(width, height))
				NewRenderer(fresh, width, height).DrawPanelView(panels, step.panel, status, step.selected, step.command, step.query, 0, false, OverviewActions{})
				for y := 0; y < height; y++ {
					for x := 0; x < width; x++ {
						if got, want := terminal.Cell(x, y), fresh.Cell(x, y); got != want {
							t.Fatalf("step %d cell(%d,%d): got %+v want %+v", i, x, y, got, want)
						}
					}
				}
				output.Reset()
				r.DrawPanelView(panels, step.panel, status, step.selected, step.command, step.query, 0, false, OverviewActions{})
				if output.Len() != 0 {
					t.Fatalf("step %d unchanged view wrote %d bytes", i, output.Len())
				}
			}
		})
	}
}

func TestScriptNavigationPreservesPanel(t *testing.T) {
	var output bytes.Buffer
	o := New(nil, 120, 40, DefaultConfig())
	o.renderer.SetOutput(&output)
	o.state.Store(int32(StateMenu))
	o.panelMode = true
	o.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
	o.status.Scripts = []ScriptInfo{{Name: "web", State: "running"}, {Name: "api", State: "running"}}
	o.draw()
	router := NewInputRouter(nil, o)
	for _, key := range []string{"Down", "Up", "j", "k"} {
		output.Reset()
		router.handleMenuKey(key)
		if strings.Contains(output.String(), ClearScreen) || strings.Contains(output.String(), "Ctrl+→ Panels") {
			t.Fatalf("key %q repainted the panel", key)
		}
	}
}

func TestPanelFrameInvalidation(t *testing.T) {
	for _, reset := range []string{"resize", "output", "enter", "exit", "clear", "visible", "frame"} {
		t.Run(reset, func(t *testing.T) {
			var output bytes.Buffer
			r := NewRenderer(&output, 80, 24)
			panels := []PanelItem{{Type: "overview", Label: "overview"}}
			draw := func() { r.DrawPanelView(panels, 0, Status{}, 0, false, "", 0, false, OverviewActions{}) }
			draw()
			switch reset {
			case "resize":
				r.SetSize(100, 30)
			case "output":
				r.SetOutput(&output)
			case "enter":
				r.EnterAltScreen()
			case "exit":
				r.ExitAltScreen()
			case "clear":
				r.ClearScreen()
			case "visible":
				r.ClearVisible()
			case "frame":
				r.WriteFrame([]byte("external frame"))
			}
			output.Reset()
			draw()
			if !strings.Contains(output.String(), ClearScreen) {
				t.Fatal("new screen retained the previous frame")
			}
		})
	}
}

func TestConfigAndScrollUsePanelFrames(t *testing.T) {
	const width, height = 100, 30
	terminal := vt10x.New(vt10x.WithSize(width, height))
	var output bytes.Buffer
	r := NewRenderer(io.MultiWriter(terminal, &output), width, height)
	editor := editorFor(t, "scripts {\n}\n")
	panels := []PanelItem{{Type: "config", Label: "config"}, {Type: "log", Label: "log"}}
	panels[1].SetContent(strings.Repeat("log output\n", 60))
	actions := OverviewActions{ConfigEditor: editor}
	for step := 0; step < 5; step++ {
		panel := 0
		if step == 1 {
			editor.Insert("x")
		}
		if step >= 2 {
			panel = 1
			panels[1].ScrollOffset = step - 2
		}
		output.Reset()
		r.DrawPanelView(panels, panel, Status{}, 0, false, "", 0, false, actions)
		if step > 0 && strings.Contains(output.String(), ClearScreen) {
			t.Fatalf("step %d cleared screen", step)
		}
		fresh := vt10x.New(vt10x.WithSize(width, height))
		NewRenderer(fresh, width, height).DrawPanelView(panels, panel, Status{}, 0, false, "", 0, false, actions)
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				if got, want := terminal.Cell(x, y), fresh.Cell(x, y); got != want {
					t.Fatalf("step %d cell(%d,%d): got %+v want %+v", step, x, y, got, want)
				}
			}
		}
	}
}
