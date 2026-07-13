package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

func TestDeveloperEventTargetIsolatesExactProxyAndProject(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	pm := proxy.NewProxyManager()
	defer pm.Shutdown(context.Background())
	for _, cfg := range []proxy.ProxyConfig{
		{ID: "web-a", Path: "/project/a", TargetURL: backend.URL},
		{ID: "web-b", Path: "/project/b", TargetURL: backend.URL},
		{ID: "api-a", Path: "/project/a", TargetURL: backend.URL},
	} {
		_, err := pm.Create(context.Background(), cfg)
		require.NoError(t, err)
	}
	b := newProxyManagerBroadcaster(pm)
	require.Equal(t, "web-a", b.developerEventTarget(protocol.DeveloperEvent{ProxyID: "web-a", ProjectPath: "/project/a"}).ID)
	require.Nil(t, b.developerEventTarget(protocol.DeveloperEvent{ProxyID: "web-a", ProjectPath: "/project/b"}))
	require.Nil(t, b.developerEventTarget(protocol.DeveloperEvent{ProjectPath: "/project/a"}), "ambiguous project must never fan out")
	require.Equal(t, "web-b", b.developerEventTarget(protocol.DeveloperEvent{ProjectPath: "/project/b"}).ID, "unique project resolves one proxy")
}

func TestDeveloperEventPairedDeliverySurvivesProxyRemovalBetweenSurfaces(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	pm := proxy.NewProxyManager()
	defer pm.Shutdown(context.Background())
	p, err := pm.Create(context.Background(), proxy.ProxyConfig{ID: "web", Path: "/project", TargetURL: backend.URL})
	require.NoError(t, err)
	b := newProxyManagerBroadcaster(pm)
	var toastEdges atomic.Int32
	b.afterDeveloperToast = func() {
		toastEdges.Add(1)
		require.NoError(t, pm.Stop(context.Background(), "web"))
	}
	b.DeliverDeveloperEvent(protocol.DeveloperEvent{Kind: "forward_collision", ProxyID: "web", ProjectPath: "/project", Severity: "warning", Title: "remapped", Message: "5173 -> 5174"})
	require.Equal(t, int32(1), toastEdges.Load())
	entries := p.Logger().Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypeDiagnostic}})
	paired := 0
	for _, entry := range entries {
		if entry.Diagnostic != nil && entry.Diagnostic.Event == "forward_collision" {
			paired++
		}
	}
	require.Equal(t, 1, paired, "diagnostic must use the same resolved target exactly once after registry removal")
}
