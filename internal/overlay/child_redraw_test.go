package overlay

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedrawAfterChildOutputDoesNotRepaintMenu(t *testing.T) {
	var out bytes.Buffer
	o := New(nil, 80, 24, DefaultConfig())
	o.renderer = NewRenderer(&out, 80, 24)
	o.panelMode = true
	o.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
	o.state.Store(int32(StateMenu))

	o.RedrawAfterChildOutput()

	if got := out.String(); got != "" {
		t.Fatalf("child redraw must not write while menu is active: %q", got)
	}
}

func TestRedrawAfterChildOutputRedrawsIndicator(t *testing.T) {
	var out bytes.Buffer
	o := New(nil, 80, 24, DefaultConfig())
	o.renderer = NewRenderer(&out, 80, 24)

	o.RedrawAfterChildOutput()

	got := out.String()
	if got == "" {
		t.Fatal("child redraw should refresh the visible indicator")
	}
	if strings.Contains(got, ClearScreen) {
		t.Fatalf("indicator refresh must not clear the screen: %q", got)
	}
}
