package daemon

import (
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

	d.registerIncidentProxyOwner(&proxy.ProxyServer{ID: "proxy-a", Path: "/project/a"})
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
