package overlay

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
)

// Key handlers run under the overlay lock (handleMenuKey takes it), and Notify
// takes the same lock to repaint while a panel is open. A handler that called
// Notify froze every later key: the input goroutine deadlocked on itself. These
// tests drive keys the way the input loop does, through handleMenuKey with the
// panel view up, and fail instead of hanging when a key never returns.

// menuRouterWithConfig builds a router whose overlay shows the panel view, as
// it does on a real terminal, with the config panel open on body.
func menuRouterWithConfig(t *testing.T, body string) (*InputRouter, *Overlay, *fakeProxyController, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, config.AgntConfigFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	ov := New(&writeRecorder{}, 80, 24, cfg)
	ov.renderer = NewRenderer(&bytes.Buffer{}, 80, 24)
	ov.state.Store(int32(StateMenu))
	ov.panelMode = true
	ov.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}

	ctrl := &fakeProxyController{projectPath: dir}
	r := NewInputRouter(&writeRecorder{}, ov)
	r.scriptController = ctrl

	ov.mu.Lock()
	err := r.openConfigPanel()
	ov.mu.Unlock()
	if err != nil {
		t.Fatalf("openConfigPanel: %v", err)
	}
	return r, ov, ctrl, path
}

// pressKey delivers key as the input loop does and fails if the handler does
// not return. A deadlocked handler is left blocked; the test still ends.
func pressKey(t *testing.T, r *InputRouter, key string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		r.handleMenuKey(key)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("key %q never returned: the overlay input is frozen (Notify under the overlay lock)", key)
	}
}

// toastTexts returns the active notifications' text.
func toastTexts(ov *Overlay) []string {
	views, _ := ov.notifications.snapshot(time.Now())
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.Text)
	}
	return out
}

func hasToast(ov *Overlay, substr string) bool {
	for _, s := range toastTexts(ov) {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func TestConfigPanel_EscapeWithUnsavedEditsDoesNotFreezeInput(t *testing.T) {
	r, ov, _, _ := menuRouterWithConfig(t, "scripts {\n}\n")
	ov.mu.Lock()
	ov.configEditor.Insert("// typed")
	ov.mu.Unlock()

	pressKey(t, r, "Escape")
	if !hasToast(ov, "unsaved config changes") {
		t.Errorf("first Escape did not warn about unsaved edits; toasts = %q", toastTexts(ov))
	}
	// The input still works: the second Escape discards.
	pressKey(t, r, "Escape")
	ov.mu.Lock()
	defer ov.mu.Unlock()
	if ov.configEditor != nil {
		t.Error("second Escape did not close the editor")
	}
}

func TestConfigPanel_SaveMessagesDoNotFreezeInput(t *testing.T) {
	cases := []struct {
		name      string
		insert    string
		reconcile error
		want      string
	}{
		{name: "saved", insert: "// ok\n", want: "saved "},
		{name: "refused", insert: " {", want: "parse"},
		{name: "saved but not applied", insert: "// ok\n", reconcile: errors.New("daemon gone"), want: "could not apply live"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ov, ctrl, _ := menuRouterWithConfig(t, "scripts {\n}\n")
			ctrl.reconcile = tc.reconcile
			ov.mu.Lock()
			ov.configEditor.Insert(tc.insert)
			ov.mu.Unlock()

			pressKey(t, r, "\x13")
			if !hasToast(ov, tc.want) {
				t.Errorf("^S toast missing %q; toasts = %q", tc.want, toastTexts(ov))
			}
			pressKey(t, r, "x") // the next key still reaches the editor
		})
	}
}

func TestPalette_MuteDoesNotFreezeInput(t *testing.T) {
	for _, cmd := range []string{"mute", "unmute"} {
		t.Run(cmd, func(t *testing.T) {
			r, ov, _, _ := menuRouterWithConfig(t, "scripts {\n}\n")
			var toggled []bool
			r.forwardingToggle = func(paused bool) { toggled = append(toggled, paused) }
			ov.mu.Lock()
			ov.panelIndex = 0
			ov.commandInput = true
			ov.commandBuffer = cmd
			ov.mu.Unlock()

			pressKey(t, r, "\r")
			if len(toggled) != 1 || toggled[0] != (cmd == "mute") {
				t.Errorf("forwarding toggle calls = %v", toggled)
			}
			if !hasToast(ov, "agent forwarding") {
				t.Errorf("no forwarding toast; toasts = %q", toastTexts(ov))
			}
			pressKey(t, r, "Escape")
		})
	}
}
