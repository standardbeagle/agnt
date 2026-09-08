package proxy

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/scope"
)

// Stop resolves a proxy by a name the caller can type -- "dev" for
// "myapp-abc1:dev:localhost-3000" -- but the registry is keyed by the whole
// compound id. Removing by what the caller typed removes nothing, so the
// stopped proxy stays listed.
//
// The list is what `proxy list` returns and what the overlay and the alert
// fan-out read. A row whose listener is closed tells a developer, and an
// agent, that a proxy is running when it is not, and the daemon's own active
// count never comes back down.
func TestProxyManager_StopByShortNameRemovesTheProxyItStopped(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()
	const fullID = "myapp-abc1:dev:localhost-3000"

	if _, err := pm.Create(ctx, ProxyConfig{
		ID:         fullID,
		TargetURL:  "http://localhost:9999",
		MaxLogSize: 100,
	}); err != nil {
		t.Fatalf("creating the proxy: %v", err)
	}

	if err := pm.Stop(ctx, "dev"); err != nil {
		t.Fatalf("stopping by short name: %v", err)
	}

	if listed := pm.ListScoped(scope.Unscoped("test asserts the whole registry")); len(listed) != 0 {
		t.Errorf("after Stop the registry still lists %d proxy/proxies: %+v", len(listed), listed[0].ID)
	}
	if got := pm.ActiveCount(); got != 0 {
		t.Errorf("active count = %d after Stop, want 0", got)
	}
	if _, err := pm.Get(fullID); err == nil {
		t.Error("the stopped proxy is still resolvable by its full id")
	}
}

// A proxy restarted under the same id while the old one is stopping must not
// be retired by the stop it did not belong to. This is the ordering the port
// forwards already hold: remove by identity, never by id alone.
func TestProxyManager_StopDoesNotRetireARestartedProxy(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()
	const id = "myapp-abc1:dev:localhost-3000"

	first, err := pm.Create(ctx, ProxyConfig{ID: id, TargetURL: "http://localhost:9999", MaxLogSize: 100})
	if err != nil {
		t.Fatalf("creating the first proxy: %v", err)
	}
	if err := first.Stop(ctx); err != nil {
		t.Fatalf("stopping the first proxy directly: %v", err)
	}

	// The restart: a fresh server registered under the same id.
	pm.proxies.Delete(id)
	pm.activeCount.Add(-1)
	second, err := pm.Create(ctx, ProxyConfig{ID: id, TargetURL: "http://localhost:9998", MaxLogSize: 100})
	if err != nil {
		t.Fatalf("creating the replacement proxy: %v", err)
	}
	defer pm.Stop(ctx, id)

	// A late Stop for the first proxy arrives now.
	pm.stopProxy(ctx, first)

	got, err := pm.Get(id)
	if err != nil {
		t.Fatalf("the replacement proxy was retired by the old proxy's stop: %v", err)
	}
	if got != second {
		t.Error("the registry holds the old proxy, not the replacement")
	}
}
