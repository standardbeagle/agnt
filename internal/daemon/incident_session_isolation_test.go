package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

func TestFireToIncidentBus_IsolatesOwningSessionAcrossAndWithinProjects(t *testing.T) {
	d := &Daemon{
		incidentBus:     incident.NewMPSCBus(nil),
		sessionRegistry: NewSessionRegistry(time.Minute),
	}
	t.Cleanup(d.incidentBus.Close)

	register := func(code, project string, startedAt time.Time) {
		require.NoError(t, d.sessionRegistry.Register(&Session{
			Code: code, ProjectPath: project, StartedAt: startedAt, Status: SessionStatusActive,
		}))
		d.addIncidentSession(code)
	}

	now := time.Now()
	register("project-b", "/project/b", now)
	register("same-project-older", "/project/a", now)
	register("project-a-owner", "/project/a", now.Add(time.Second))

	d.registerIncidentProxyOwner("proxy-a", "project-a-owner")
	d.fireToIncidentBus(chaosHTTPEntry(500, false, false), "proxy-a")

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("project-a-owner", incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond, "owning session did not receive production HTTP incident")

	for _, code := range []string{"project-b", "same-project-older"} {
		entries, _ := d.incidentBus.QuerySession(code, incident.QueryFilter{})
		require.Empty(t, entries, "incident leaked into session %s", code)
	}
}

func TestProxyLoggerCallbackUsesImmutableOwnerAndDropsUnresolved(t *testing.T) {
	d := &Daemon{incidentBus: incident.NewMPSCBus(nil), sessionRegistry: NewSessionRegistry(time.Minute), eventHub: NewEventHub()}
	t.Cleanup(d.incidentBus.Close)
	for _, code := range []string{"owner", "same-project-peer", "other-project"} {
		d.addIncidentSession(code)
	}

	server, err := proxy.NewProxyServer(proxy.ProxyConfig{ID: "owned-proxy", TargetURL: "http://127.0.0.1:1", Path: "/project/a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	d.registerIncidentProxyOwner(server.ID, "owner")
	d.wireProxyLogger(server)
	server.Logger().LogDiagnostic(proxy.ProxyDiagnostic{Level: proxy.DiagnosticError, Category: "proxy", Event: "config_error", Message: "invalid proxy config"})

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("owner", incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond)
	for _, code := range []string{"same-project-peer", "other-project"} {
		entries, _ := d.incidentBus.QuerySession(code, incident.QueryFilter{})
		require.Empty(t, entries)
	}

	unowned, err := proxy.NewProxyServer(proxy.ProxyConfig{ID: "unowned-proxy", TargetURL: "http://127.0.0.1:1", Path: "/project/a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = unowned.Stop(context.Background()) })
	d.wireProxyLogger(unowned)
	unowned.Logger().LogDiagnostic(proxy.ProxyDiagnostic{Level: proxy.DiagnosticError, Category: "proxy", Event: "config_error", Message: "invalid proxy config"})
	time.Sleep(30 * time.Millisecond)
	entries, _ := d.incidentBus.QuerySession("owner", incident.QueryFilter{})
	require.Len(t, entries, 1, "unresolved proxy owner must deliver to none")
}
