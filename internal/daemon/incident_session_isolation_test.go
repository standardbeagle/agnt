package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/overlay"
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
	for _, session := range []*Session{
		{Code: "owner", ProjectPath: "/project/a", Status: SessionStatusActive},
		{Code: "same-project-peer", ProjectPath: "/project/a", Status: SessionStatusActive},
		{Code: "other-project", ProjectPath: "/project/b", Status: SessionStatusActive},
	} {
		require.NoError(t, d.sessionRegistry.Register(session))
		code := session.Code
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

func TestProcessAlertCallbackUsesImmutableOwnerAndDropsUnresolved(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	for _, session := range []*Session{
		{Code: "owner", ProjectPath: "/project/a", Status: SessionStatusActive},
		{Code: "same-project-peer", ProjectPath: "/project/a", Status: SessionStatusActive},
		{Code: "other-project", ProjectPath: "/project/b", Status: SessionStatusActive},
	} {
		require.NoError(t, d.sessionRegistry.Register(session))
		d.addIncidentSession(session.Code)
	}

	match := func(processID string) *overlay.AlertMatch {
		owner, _ := d.incidentProcessOwner.Load(processID)
		return &overlay.AlertMatch{
			Pattern: &overlay.AlertPattern{ID: "panic", Severity: overlay.AlertSeverityError, Category: "go", Description: "panic"},
			Line:    "panic: callback isolation", ScriptID: processID, LifetimeToken: ownerAsIncidentResource(owner),
		}
	}
	d.registerIncidentProcessOwner("owned-process", "owner")
	d.ingestProcessAlert(match("owned-process"), time.Now())
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("owner", incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond)
	for _, code := range []string{"same-project-peer", "other-project"} {
		entries, _ := d.incidentBus.QuerySession(code, incident.QueryFilter{})
		require.Empty(t, entries)
	}

	d.ingestProcessAlert(match("unowned-process"), time.Now())
	time.Sleep(30 * time.Millisecond)
	entries, _ := d.incidentBus.QuerySession("owner", incident.QueryFilter{})
	require.Len(t, entries, 1, "unresolved process owner must deliver to none")
}

func TestIncidentOwnerCanBeReboundOnlyAfterResourceTeardown(t *testing.T) {
	d := &Daemon{}
	d.registerIncidentProxyOwner("reused", "first")
	d.registerIncidentProxyOwner("reused", "second")
	owner, _ := d.incidentProxyOwner.Load("reused")
	require.Equal(t, "first", ownerAsIncidentResource(owner).sessionCode, "owner must stay immutable during one resource lifetime")

	d.retireIncidentProxyOwner("reused")
	d.registerIncidentProxyOwner("reused", "second")
	owner, _ = d.incidentProxyOwner.Load("reused")
	require.Equal(t, "second", ownerAsIncidentResource(owner).sessionCode, "a recreated resource must bind a fresh owner")

	d.registerIncidentProcessOwner("reused", "first")
	d.retireIncidentProcessOwner("reused")
	d.registerIncidentProcessOwner("reused", "second")
	owner, _ = d.incidentProcessOwner.Load("reused")
	require.Equal(t, "second", ownerAsIncidentResource(owner).sessionCode)
}

func TestProxyLoggerDelayedCallbackRejectsReusedIDLifetime(t *testing.T) {
	d := &Daemon{incidentBus: incident.NewMPSCBus(nil), sessionRegistry: NewSessionRegistry(time.Minute), eventHub: NewEventHub()}
	t.Cleanup(d.incidentBus.Close)
	for _, code := range []string{"owner-a", "owner-b"} {
		d.addIncidentSession(code)
	}

	d.registerIncidentProxyOwner("reused-proxy", "owner-a")
	oldServer, err := proxy.NewProxyServer(proxy.ProxyConfig{ID: "reused-proxy", TargetURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = oldServer.Stop(context.Background()) })
	d.wireProxyLogger(oldServer)
	d.retireIncidentProxyOwner("reused-proxy")
	d.registerIncidentProxyOwner("reused-proxy", "owner-b")

	oldServer.Logger().LogDiagnostic(proxy.ProxyDiagnostic{Level: proxy.DiagnosticError, Category: "proxy", Event: "config_error", Message: "late old lifetime"})
	time.Sleep(30 * time.Millisecond)
	for _, code := range []string{"owner-a", "owner-b"} {
		entries, _ := d.incidentBus.QuerySession(code, incident.QueryFilter{})
		require.Empty(t, entries, "stale proxy lifetime delivered to %s", code)
	}

	newServer, err := proxy.NewProxyServer(proxy.ProxyConfig{ID: "reused-proxy", TargetURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = newServer.Stop(context.Background()) })
	d.wireProxyLogger(newServer)
	newServer.Logger().LogDiagnostic(proxy.ProxyDiagnostic{Level: proxy.DiagnosticError, Category: "proxy", Event: "config_error", Message: "current lifetime"})
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("owner-b", incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond)
}

func TestProcessScannerDelayedCallbackRejectsReusedIDLifetime(t *testing.T) {
	d := &Daemon{incidentBus: incident.NewMPSCBus(nil)}
	t.Cleanup(d.incidentBus.Close)
	for _, code := range []string{"owner-a", "owner-b"} {
		d.addIncidentSession(code)
	}
	d.alertScanner = overlay.NewAlertScanner(overlay.AlertScannerConfig{
		BatchWindow: 10 * time.Millisecond,
		OnAlert: func(batch *overlay.AlertBatch) {
			for _, match := range batch.Matches {
				d.ingestProcessIncident(match)
			}
		},
	})
	t.Cleanup(d.alertScanner.Stop)

	d.registerIncidentProcessOwner("reused-process", "owner-a")
	owner, _ := d.incidentProcessOwner.Load("reused-process")
	d.alertScanner.ProcessLineWithLifetime("panic: old lifetime", "reused-process", ownerAsIncidentResource(owner))
	d.retireIncidentProcessOwner("reused-process")
	d.registerIncidentProcessOwner("reused-process", "owner-b")
	time.Sleep(50 * time.Millisecond)
	for _, code := range []string{"owner-a", "owner-b"} {
		entries, _ := d.incidentBus.QuerySession(code, incident.QueryFilter{})
		require.Empty(t, entries, "stale process lifetime delivered to %s", code)
	}

	owner, _ = d.incidentProcessOwner.Load("reused-process")
	d.alertScanner.ProcessLineWithLifetime("fatal: current lifetime", "reused-process", ownerAsIncidentResource(owner))
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("owner-b", incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond)
}
