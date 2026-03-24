package overlay

import (
	"testing"
)

// These tests verify that the session log panel exists and works correctly.
// The log panel shows all startup log entries from the daemon, providing
// a complete diagnostic view of what happened during session setup.

// TestLogPanel_ExistsInPanelList verifies that buildPanelItems always
// includes a "log" panel after the overview panel.
func TestLogPanel_ExistsInPanelList(t *testing.T) {
	o := &Overlay{}
	o.status = Status{
		Scripts: []ScriptInfo{
			{Name: "dev", State: "running"},
		},
	}
	o.buildPanelItems()

	if len(o.panelItems) < 2 {
		t.Fatalf("Expected at least 2 panels (overview + log), got %d", len(o.panelItems))
	}

	// Log panel should be the last panel (after overview and scripts)
	logPanel := o.panelItems[len(o.panelItems)-1]
	if logPanel.Type != "log" {
		t.Errorf("Last panel should be type 'log', got %q", logPanel.Type)
	}
	if logPanel.Label != "log" {
		t.Errorf("Log panel label should be 'log', got %q", logPanel.Label)
	}
}

// TestLogPanel_ExistsEvenWithNoScripts verifies the log panel exists
// when there are no scripts at all (empty project or failed config load).
func TestLogPanel_ExistsEvenWithNoScripts(t *testing.T) {
	o := &Overlay{}
	o.status = Status{}
	o.buildPanelItems()

	found := false
	for _, p := range o.panelItems {
		if p.Type == "log" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Log panel should exist even with no scripts")
	}
}

// TestLogPanel_ContentFromStartupLog verifies that the log panel's content
// is populated from status.StartupLog entries.
func TestLogPanel_ContentFromStartupLog(t *testing.T) {
	o := &Overlay{}
	o.status = Status{
		StartupLog: []StartupLogEntry{
			{ScriptName: "dev", Level: "info", EventType: "started", Message: "dev started"},
			{ScriptName: "api", Level: "error", EventType: "start_failed", Message: "port 5000 in use"},
		},
	}
	o.buildPanelItems()

	var logPanel *PanelItem
	for i := range o.panelItems {
		if o.panelItems[i].Type == "log" {
			logPanel = &o.panelItems[i]
			break
		}
	}
	if logPanel == nil {
		t.Fatal("Log panel not found")
	}

	if logPanel.Content == "" {
		t.Error("Log panel content should be populated from StartupLog entries")
	}

	// Should contain both entries
	if !containsSubstring(logPanel.Content, "dev started") {
		t.Errorf("Log panel should contain 'dev started', got: %s", logPanel.Content)
	}
	if !containsSubstring(logPanel.Content, "port 5000 in use") {
		t.Errorf("Log panel should contain 'port 5000 in use', got: %s", logPanel.Content)
	}
}

// TestLogPanel_UpdatesOnStatusChange verifies that rebuilding panels
// updates the log panel content when new startup log entries arrive.
func TestLogPanel_UpdatesOnStatusChange(t *testing.T) {
	o := &Overlay{}
	o.status = Status{
		StartupLog: []StartupLogEntry{
			{ScriptName: "dev", Level: "info", EventType: "started", Message: "dev started"},
		},
	}
	o.buildPanelItems()

	// Simulate new status with additional log entry
	o.status = Status{
		StartupLog: []StartupLogEntry{
			{ScriptName: "dev", Level: "info", EventType: "started", Message: "dev started"},
			{ScriptName: "api", Level: "info", EventType: "started", Message: "api started"},
		},
	}
	o.buildPanelItems()

	var logPanel *PanelItem
	for i := range o.panelItems {
		if o.panelItems[i].Type == "log" {
			logPanel = &o.panelItems[i]
			break
		}
	}
	if logPanel == nil {
		t.Fatal("Log panel not found")
	}

	if !containsSubstring(logPanel.Content, "api started") {
		t.Errorf("Log panel should contain new entry 'api started', got: %s", logPanel.Content)
	}
}

// TestLogPanel_NotCloseable verifies the log panel cannot be closed by the user.
func TestLogPanel_NotCloseable(t *testing.T) {
	o := &Overlay{}
	o.status = Status{}
	o.buildPanelItems()

	for _, p := range o.panelItems {
		if p.Type == "log" {
			if p.IsDone() {
				t.Error("Log panel should not be closeable")
			}
			return
		}
	}
	t.Error("Log panel not found")
}

// TestLogPanel_ScrollWorks verifies that the log panel supports scrolling.
func TestLogPanel_ScrollWorks(t *testing.T) {
	o := &Overlay{}
	entries := make([]StartupLogEntry, 50)
	for i := range entries {
		entries[i] = StartupLogEntry{
			ScriptName: "dev",
			Level:      "info",
			EventType:  "started",
			Message:    "line",
		}
	}
	o.status = Status{StartupLog: entries}
	o.buildPanelItems()

	var logPanel *PanelItem
	for i := range o.panelItems {
		if o.panelItems[i].Type == "log" {
			logPanel = &o.panelItems[i]
			break
		}
	}
	if logPanel == nil {
		t.Fatal("Log panel not found")
	}

	if logPanel.ContentLines() == 0 {
		t.Error("Log panel should have content lines for scrolling")
	}

	// Scroll offset should start at 0 (bottom)
	if logPanel.ScrollOffset != 0 {
		t.Errorf("Initial scroll offset should be 0, got %d", logPanel.ScrollOffset)
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
