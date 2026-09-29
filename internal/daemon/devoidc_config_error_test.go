package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// TestApplyDevOIDC_ConfigLoadFailureIsVisible pins that a malformed
// .agnt.kdl reaching applyDevOIDC (the dev-oidc install chokepoint every
// proxy-creation path funnels through, hub_helpers.go's wireProxyLogger)
// produces a visible diagnostic on the proxy's own log, not just
// server.SetDevOIDC(nil). Before this fix the failure was swallowed with a
// comment claiming "reported by the paths that start scripts and proxies
// from it" — true only for autostart's own config load, not for this
// independent reload that runs on every proxy creation regardless of how it
// was triggered (daemon-architecture.md § Silent Failure Prohibition: an
// event must reach the agent/session log, not just be inferred elsewhere).
func TestApplyDevOIDC_ConfigLoadFailureIsVisible(t *testing.T) {
	d := newTestDaemon(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte("not valid kdl {{{"), 0o644))

	server, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "devoidc-broken-config",
		TargetURL:  "http://127.0.0.1:1",
		ListenPort: 0,
		MaxLogSize: 50,
		Path:       dir,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "devoidc-broken-config") })

	d.applyDevOIDC(server)

	entries := server.Logger().Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypeDiagnostic}})
	found := false
	for _, e := range entries {
		if e.Diagnostic != nil && e.Diagnostic.Event == "dev_oidc_config_load_failed" {
			found = true
		}
	}
	require.True(t, found, "expected a dev_oidc_config_load_failed diagnostic, got %+v", entries)
}
