//go:build unix

// newTestDaemon (the shared daemon test harness) is unix-tagged, so this test
// is too. The reportPortKills logic under test is OS-agnostic; exercising it on
// unix is sufficient coverage.
package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// TestReportPortKills_NotifiesKillerAndVictim verifies the dev-server-conflict
// reporting: when an auto-kill reclaims a port, the killer project sees what it
// killed AND any other project whose proxy targets that port is told its dev
// server was killed out from under it. Without the victim notice the developer
// sees "two projects serving the same site" with no explanation.
func TestReportPortKills_NotifiesKillerAndVictim(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	u, err := url.Parse(backend.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	d := newTestDaemon(t)
	d.startupErrorStore = NewStartupLogStore(50)

	const victimProject = "/home/u/work/gyldcraft"
	const killerProject = "/home/u/work/space"

	// The victim's proxy targets the backend whose port is about to be killed.
	_, err = d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "gyld:web",
		TargetURL:  backend.URL,
		Path:       victimProject,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "gyld:web") })

	killResults := []KillResult{{
		PortConflict: PortConflict{ScriptName: "dev", Port: port, ProcessName: "node", PIDs: []int{4242}},
		Killed:       true,
	}}
	d.reportPortKills(killerProject, killResults)

	entries := d.startupErrorStore.Recent(time.Hour, 100)
	killerPrefix := makeProcessID(killerProject, "")
	victimPrefix := makeProcessID(victimProject, "")

	var killer, victim *StartupLogEntry
	for _, e := range entries {
		if e.EventType == "port_conflict_killed" && strings.HasPrefix(e.ProcessID, killerPrefix) {
			killer = e
		}
		if e.EventType == "dev_server_killed" && strings.HasPrefix(e.ProcessID, victimPrefix) {
			victim = e
		}
	}

	require.NotNil(t, killer, "killer project must get a port_conflict_killed warning")
	require.NotNil(t, victim, "victim project must get a dev_server_killed warning")
	require.Equal(t, "warning", killer.Level, "kill is a destructive side effect, warn not info")
	require.Equal(t, "warning", victim.Level)
	require.Equal(t, port, killer.Port)
	require.Equal(t, port, victim.Port)
	require.Contains(t, victim.Message, "space", "victim notice must name the killer project")
}

// TestReportPortKills_NoVictimWhenNoProxy verifies the killer-only path: with
// no other project proxying the reclaimed port, only the killer notice fires,
// and a failed kill produces no notices at all.
func TestReportPortKills_NoVictimWhenNoProxy(t *testing.T) {
	d := newTestDaemon(t)
	d.startupErrorStore = NewStartupLogStore(50)

	const killerProject = "/home/u/work/space"

	// Killed:false → nothing reported.
	d.reportPortKills(killerProject, []KillResult{{
		PortConflict: PortConflict{ScriptName: "dev", Port: 5173, ProcessName: "node", PIDs: []int{1}},
		Killed:       false,
	}})
	require.Empty(t, d.startupErrorStore.Recent(time.Hour, 100), "failed kill must not emit notices")

	// Killed:true with no matching proxy → killer notice only, no victim.
	d.reportPortKills(killerProject, []KillResult{{
		PortConflict: PortConflict{ScriptName: "dev", Port: 5173, ProcessName: "node", PIDs: []int{1}},
		Killed:       true,
	}})
	entries := d.startupErrorStore.Recent(time.Hour, 100)
	var killers, victims int
	for _, e := range entries {
		switch e.EventType {
		case "port_conflict_killed":
			killers++
		case "dev_server_killed":
			victims++
		}
	}
	require.Equal(t, 1, killers, "exactly one killer notice")
	require.Equal(t, 0, victims, "no victim when no project proxies the port")
}
