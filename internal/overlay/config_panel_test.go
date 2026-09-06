package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

// routerWithConfig builds an overlay in panel mode with the config panel open.
func routerWithConfig(t *testing.T, body string) (*InputRouter, *fakeProxyController, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, config.AgntConfigFileName)
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	ctrl := &fakeProxyController{projectPath: dir}
	o := &Overlay{panelMode: true, panelItems: []PanelItem{{Type: "overview", Label: "overview"}}}
	r := &InputRouter{overlay: o, scriptController: ctrl}
	if err := r.openConfigPanel(); err != nil {
		t.Fatalf("openConfigPanel: %v", err)
	}
	return r, ctrl, path
}

func TestOpenConfigPanel_FocusesTheNewPanel(t *testing.T) {
	r, _, path := routerWithConfig(t, "scripts {\n}\n")
	if !r.isConfigPanel() {
		t.Fatal("config panel is not the active panel after opening it")
	}
	if r.overlay.configEditor == nil || r.overlay.configEditor.Path() != path {
		t.Errorf("editor not loaded for %s", path)
	}
}

func TestOpenConfigPanel_ReopeningKeepsUnsavedEdits(t *testing.T) {
	// Reloading from disk here would silently discard whatever the developer
	// had typed, which is the one thing an editor must never do.
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	r.overlay.configEditor.Insert("// typed")
	if err := r.openConfigPanel(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !strings.Contains(r.overlay.configEditor.Text(), "// typed") {
		t.Error("reopening the panel discarded unsaved edits")
	}
}

// TestConfigPanel_CapturesPrintableKeys is the reason the editor intercepts
// before panel navigation: 'q' and 'x' are panel-browser commands, and typing a
// word containing them would otherwise close the panel mid-edit.
func TestConfigPanel_CapturesPrintableKeys(t *testing.T) {
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	for _, key := range []string{"q", "x", "1", ":"} {
		if !r.handleConfigKey(key) {
			t.Errorf("key %q was not consumed by the editor", key)
		}
	}
	if got := r.overlay.configEditor.Lines()[0]; !strings.HasPrefix(got, "qx1:") {
		t.Errorf("typed keys did not reach the buffer: %q", got)
	}
	if r.overlay.configEditor == nil {
		t.Fatal("editor closed while typing")
	}
}

func TestConfigPanel_SaveKeyWritesAndReconciles(t *testing.T) {
	r, ctrl, path := routerWithConfig(t, "scripts {\n}\n")
	r.overlay.configEditor.MoveCursor(1, 0)
	r.overlay.configEditor.MoveToLineStart()
	r.overlay.configEditor.Insert("    dev {\n    run \"npm run dev\"\n}\n")

	if !r.handleConfigKey("\x13") { // Ctrl+S arrives as a raw control byte
		t.Fatal("Ctrl+S was not consumed")
	}
	cfg, err := config.LoadAgntConfigFile(path)
	if err != nil {
		t.Fatalf("saved config does not load: %v", err)
	}
	if cfg.Scripts["dev"] == nil {
		t.Error("edit was not written")
	}
	// A saved config the daemon has not read yet is only half the job.
	if !ctrl.reconciled {
		t.Error("save did not apply the config live")
	}
}

func TestConfigPanel_SaveOfBrokenConfigDoesNotReconcile(t *testing.T) {
	original := "scripts {\n}\n"
	r, ctrl, path := routerWithConfig(t, original)
	r.overlay.configEditor.MoveToLineEnd()
	r.overlay.configEditor.Insert(" {") // unbalanced

	r.handleConfigKey("\x13")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != original {
		t.Errorf("a config that does not parse was written:\n%s", body)
	}
	if ctrl.reconciled {
		t.Error("a refused save still asked the daemon to reload")
	}
}

// TestConfigPanel_EscapeGuardsUnsavedEdits: a stray Escape must not throw away
// typed config, but a deliberate second one must still work.
func TestConfigPanel_EscapeGuardsUnsavedEdits(t *testing.T) {
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	r.overlay.configEditor.Insert("// typed")

	r.handleConfigKey("Escape")
	if r.overlay.configEditor == nil {
		t.Fatal("first Escape discarded unsaved edits")
	}
	r.handleConfigKey("Escape")
	if r.overlay.configEditor != nil {
		t.Error("second Escape did not close the panel")
	}
	for _, p := range r.overlay.panelItems {
		if p.Type == "config" {
			t.Error("panel still present after closing")
		}
	}
}

func TestConfigPanel_TypingDisarmsTheDiscardGuard(t *testing.T) {
	// Otherwise an Escape pressed minutes ago would still be "the first press"
	// and the next one would discard without warning.
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	r.overlay.configEditor.Insert("// typed")

	r.handleConfigKey("Escape")
	r.handleConfigKey("x")
	r.handleConfigKey("Escape")
	if r.overlay.configEditor == nil {
		t.Error("Escape after an intervening keypress discarded the buffer without re-warning")
	}
}

func TestConfigPanel_EscapeClosesImmediatelyWhenClean(t *testing.T) {
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	r.handleConfigKey("Escape")
	if r.overlay.configEditor != nil {
		t.Error("a clean buffer should close on the first Escape")
	}
}

func TestConfigPanel_SurvivesAStatusRebuild(t *testing.T) {
	// Panels are rebuilt from daemon status; without re-asserting the config
	// panel the next refresh would drop it out from under an open edit.
	r, _, _ := routerWithConfig(t, "scripts {\n}\n")
	r.overlay.configEditor.Insert("// typed")

	r.overlay.UpdateStatus(Status{})
	r.overlay.buildPanelItems()

	found := false
	for _, p := range r.overlay.panelItems {
		if p.Type == "config" {
			found = true
		}
	}
	if !found {
		t.Error("status rebuild dropped the config panel")
	}
	if !strings.Contains(r.overlay.configEditor.Text(), "// typed") {
		t.Error("status rebuild lost the buffer")
	}
}

func TestOpenConfigPanel_MissingProjectPathIsLoud(t *testing.T) {
	ctrl := &fakeProxyController{projectPath: ""}
	o := &Overlay{panelMode: true}
	r := &InputRouter{overlay: o, scriptController: ctrl}
	if err := r.openConfigPanel(); err == nil {
		t.Error("opening the editor with no project directory was accepted")
	}
}
