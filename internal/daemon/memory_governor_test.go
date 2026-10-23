package daemon

import (
	"context"
	"strings"

	"github.com/standardbeagle/agnt/internal/proxy"
	"testing"

	"github.com/standardbeagle/agnt/internal/memgov"
	"github.com/stretchr/testify/require"
)

// The daemon's reclaimers must tolerate a daemon with no proxies or incident
// activity, and a governor built on one must report them.
func TestMemoryGovernor_ReclaimersRunOnAnIdleDaemon(t *testing.T) {
	d, _ := newBootedDaemon(t)
	g := memgov.New(memgov.Config{})
	g.Register(proxyHistoryReclaimer{d})
	g.Register(incidentDedupReclaimer{d})

	require.Len(t, g.Stats().Reclaimers, 2)
	for _, l := range []memgov.Level{memgov.Soft, memgov.Hard} {
		require.NotPanics(t, func() {
			proxyHistoryReclaimer{d}.Reclaim(l)
			incidentDedupReclaimer{d}.Reclaim(l)
		})
	}
}

func TestMemoryGovernor_ProxyHistoryIsTrimmedUnderHardReclaim(t *testing.T) {
	d, _, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	p, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: "mem-proxy", TargetURL: ephemeralTargetURL(t), ListenPort: -1, Path: dir,
	})
	require.NoError(t, err)
	for i := 0; i < 400; i++ {
		p.Logger().LogCustom(proxy.CustomLog{Level: "info", Message: strings.Repeat("m", 2000)})
	}
	r := proxyHistoryReclaimer{d}
	before := p.Logger().Stats().AvailableEntries
	require.GreaterOrEqual(t, before, int64(400))

	r.Reclaim(memgov.Hard)
	after := p.Logger().Stats().AvailableEntries
	require.Less(t, after, before, "Hard reclaim must trim the proxy's traffic log")
}
