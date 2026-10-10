package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// TestHubBrowserAndAutomationList_RouteThroughScopeGate pins that BROWSER LIST
// and AUTOMATION LIST use the session-scope chokepoint: a session-less call that
// names no project fails loud instead of listing every project's resources,
// while an explicit directory or global:true resolves.
func TestHubBrowserAndAutomationList_RouteThroughScopeGate(t *testing.T) {
	_ = NewForTest(t, DaemonConfig{MaxClients: 10, WriteTimeout: 5 * time.Second})
	d := NewForTest(t, DaemonConfig{MaxClients: 10, WriteTimeout: 5 * time.Second})

	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	lists := map[string]func(protocol.DirectoryFilter) (map[string]interface{}, error){
		"BROWSER":    client.BrowserList,
		"AUTOMATION": client.AutomationList,
	}
	for verb, list := range lists {
		t.Run(verb, func(t *testing.T) {
			_, err := list(protocol.DirectoryFilter{})
			if err == nil {
				t.Fatalf("%s LIST must reject a session-less non-global call", verb)
			}
			if !strings.Contains(err.Error(), "no session attached") {
				t.Errorf("expected the no-session scope error, got: %v", err)
			}
			if _, err := list(protocol.DirectoryFilter{Directory: t.TempDir()}); err != nil {
				t.Errorf("%s LIST with a directory must resolve: %v", verb, err)
			}
			if _, err := list(protocol.DirectoryFilter{Global: true}); err != nil {
				t.Errorf("%s LIST with global:true must resolve: %v", verb, err)
			}
		})
	}
}
