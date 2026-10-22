package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startUnsupported drives Manager.Start through an unknown provider so no
// subprocess is spawned: tunnel.Start CAS-es Idle->Starting, hits the default
// branch, sets StateFailed, and returns an error. The manager deletes the
// entry and returns the error without incrementing active or spawning the
// done-watcher goroutine.
func TestManager_Start(t *testing.T) {
	ctx := context.Background()

	t.Run("unsupported provider returns error, nothing stored", func(t *testing.T) {
		m := NewManager()
		tun, err := m.Start(ctx, "id1", Config{Provider: Provider("bogus"), LocalPort: 1})
		require.Error(t, err)
		assert.Nil(t, tun)
		assert.Contains(t, err.Error(), "unsupported tunnel provider")
		assert.Equal(t, 0, m.ActiveCount(), "active must not increment on failed start")
		_, err = m.Get("id1")
		assert.ErrorIs(t, err, ErrTunnelNotFound, "failed tunnel must not be stored")
	})

	t.Run("duplicate id returns ErrTunnelExists", func(t *testing.T) {
		m := NewManager()
		// Seed a tunnel directly into the registry so Start sees an existing id.
		seeded := New(Config{ID: "dup", Provider: ProviderCloudflare, LocalPort: 1})
		m.tunnels.Store("dup", seeded)
		tun, err := m.Start(ctx, "dup", Config{Provider: Provider("bogus"), LocalPort: 2})
		assert.Nil(t, tun)
		assert.ErrorIs(t, err, ErrTunnelExists)
		// Seeded tunnel must remain untouched.
		got, gErr := m.Get("dup")
		require.NoError(t, gErr)
		assert.Same(t, seeded, got)
	})

	t.Run("shutting down rejects Start", func(t *testing.T) {
		m := NewManager()
		require.NoError(t, m.Shutdown(ctx)) // sets shuttingDown
		tun, err := m.Start(ctx, "x", Config{Provider: Provider("bogus"), LocalPort: 1})
		assert.Nil(t, tun)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shutting down")
	})
}

// connectedTunnel builds a tunnel already in the Connected state with a URL,
// stored directly in the manager registry. No subprocess is involved.
func connectedTunnel(id, path, url string) *Tunnel {
	t := New(Config{ID: id, Provider: ProviderCloudflare, LocalPort: 1, Path: path})
	t.setPublicURL(url)
	t.setState(StateConnected)
	return t
}

func TestManager_GetWithPathFilter(t *testing.T) {
	t.Run("exact match wins over fuzzy", func(t *testing.T) {
		m := NewManager()
		exact := connectedTunnel("hash:web:8080", "/proj", "https://a.trycloudflare.com")
		m.tunnels.Store("hash:web:8080", exact)
		got, err := m.Get("hash:web:8080")
		require.NoError(t, err)
		assert.Same(t, exact, got)
		assert.Equal(t, StateConnected, got.State())
	})

	t.Run("fuzzy match on compound component", func(t *testing.T) {
		m := NewManager()
		tun := connectedTunnel("abc123:web:8080", "/proj", "https://b.trycloudflare.com")
		m.tunnels.Store("abc123:web:8080", tun)
		got, err := m.Get("web")
		require.NoError(t, err)
		assert.Same(t, tun, got)
		// port component also matches.
		got2, err := m.Get("8080")
		require.NoError(t, err)
		assert.Same(t, tun, got2)
	})

	t.Run("not found returns ErrTunnelNotFound", func(t *testing.T) {
		m := NewManager()
		m.tunnels.Store("abc:web:8080", connectedTunnel("abc:web:8080", "/p", "u"))
		got, err := m.Get("nope")
		assert.Nil(t, got)
		assert.ErrorIs(t, err, ErrTunnelNotFound)
	})

	t.Run("ambiguous returns ErrTunnelAmbiguous", func(t *testing.T) {
		m := NewManager()
		m.tunnels.Store("h1:web:8080", connectedTunnel("h1:web:8080", "/p1", "u1"))
		m.tunnels.Store("h2:web:9090", connectedTunnel("h2:web:9090", "/p2", "u2"))
		got, err := m.Get("web")
		assert.Nil(t, got)
		assert.ErrorIs(t, err, ErrTunnelAmbiguous)
	})

	t.Run("path filter excludes non-matching, dot is no-filter", func(t *testing.T) {
		m := NewManager()
		m.tunnels.Store("h1:web:8080", connectedTunnel("h1:web:8080", "/projA", "u1"))
		m.tunnels.Store("h2:web:9090", connectedTunnel("h2:web:9090", "/projB", "u2"))

		// Without filter both match -> ambiguous.
		_, err := m.GetWithPathFilter("web", "")
		assert.ErrorIs(t, err, ErrTunnelAmbiguous)

		// "." is treated as no-filter -> still ambiguous.
		_, err = m.GetWithPathFilter("web", ".")
		assert.ErrorIs(t, err, ErrTunnelAmbiguous)

		// Filtering to /projA leaves exactly one candidate. Trailing slash is
		// normalized away so it still matches.
		got, err := m.GetWithPathFilter("web", "/projA/")
		require.NoError(t, err)
		assert.Equal(t, "/projA", got.Path())

		// Filtering to a path with no candidate -> not found.
		_, err = m.GetWithPathFilter("web", "/nowhere")
		assert.ErrorIs(t, err, ErrTunnelNotFound)
	})

	t.Run("exact match ignores path filter", func(t *testing.T) {
		m := NewManager()
		tun := connectedTunnel("h1:web:8080", "/projA", "u1")
		m.tunnels.Store("h1:web:8080", tun)
		// Even with a mismatched filter, exact id lookup succeeds.
		got, err := m.GetWithPathFilter("h1:web:8080", "/totally-different")
		require.NoError(t, err)
		assert.Same(t, tun, got)
	})
}

func TestManager_ListAndListByPath(t *testing.T) {
	m := NewManager()
	m.tunnels.Store("h1:web:8080", connectedTunnel("h1:web:8080", "/projA", "https://a.trycloudflare.com"))
	m.tunnels.Store("h2:api:9090", connectedTunnel("h2:api:9090", "/projB", "https://b.trycloudflare.com"))

	all := m.List()
	assert.Len(t, all, 2)

	// Empty filter == List.
	assert.Len(t, m.ListByPath(""), 2)

	// Filter to one path, trailing slash normalized.
	a := m.ListByPath("/projA/")
	require.Len(t, a, 1)
	assert.Equal(t, "/projA", a[0].Path)
	assert.Equal(t, "h1:web:8080", a[0].ID)
	assert.Equal(t, "connected", a[0].State)
	assert.Equal(t, "https://a.trycloudflare.com", a[0].PublicURL)

	// Filter to nonexistent path -> empty.
	assert.Empty(t, m.ListByPath("/none"))
}

func TestManager_StopByProjectPath(t *testing.T) {
	t.Run("no match returns nil nil", func(t *testing.T) {
		m := NewManager()
		// done is closed so any accidental Stop wouldn't block; but no match expected.
		ids, err := m.StopByProjectPath(context.Background(), "/no-such-path")
		assert.Nil(t, ids)
		assert.NoError(t, err)
	})

	t.Run("stops matching path, returns ids", func(t *testing.T) {
		m := NewManager()
		t1 := connectedTunnel("h1:web:8080", "/projA", "u1")
		close(t1.done) // Stop's select on done returns immediately -> clean stop.
		t2 := connectedTunnel("h2:api:9090", "/projB", "u2")
		close(t2.done)
		m.tunnels.Store("h1:web:8080", t1)
		m.tunnels.Store("h2:api:9090", t2)

		ids, err := m.StopByProjectPath(context.Background(), "/projA/")
		require.NoError(t, err)
		require.Len(t, ids, 1)
		assert.Equal(t, "h1:web:8080", ids[0])
		assert.Equal(t, StateStopped, t1.State())
		// Non-matching tunnel untouched.
		assert.NotEqual(t, StateStopped, t2.State())
	})

	t.Run("ctx cancellation surfaces error", func(t *testing.T) {
		m := NewManager()
		// Tunnel whose done never closes -> Stop blocks on ctx.
		blocker := connectedTunnel("h1:web:8080", "/projA", "u1")
		m.tunnels.Store("h1:web:8080", blocker)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		ids, err := m.StopByProjectPath(ctx, "/projA")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Empty(t, ids, "blocked stop produces no stopped ids")
	})

	t.Run("errors.Join aggregates multiple stop failures", func(t *testing.T) {
		m := NewManager()
		b1 := connectedTunnel("h1:web:8080", "/projA", "u1")
		b2 := connectedTunnel("h2:api:9090", "/projA", "u2")
		m.tunnels.Store("h1:web:8080", b1)
		m.tunnels.Store("h2:api:9090", b2)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		ids, err := m.StopByProjectPath(ctx, "/projA")
		require.Error(t, err)
		// Both stops time out; joined error contains the deadline cause and the
		// aggregation path (errs branch) appends ctx.Err once.
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Empty(t, ids)
	})
}

// TestManager_StopAllVsShutdown asserts the load-bearing distinction: Shutdown
// latches shuttingDown (Start rejected afterward) while StopAll does not (Start
// permitted afterward).
func TestManager_StopAllVsShutdown(t *testing.T) {
	ctx := context.Background()

	t.Run("StopAll does not latch shuttingDown", func(t *testing.T) {
		m := NewManager()
		tun := connectedTunnel("h1:web:8080", "/projA", "u1")
		close(tun.done)
		m.tunnels.Store("h1:web:8080", tun)

		require.NoError(t, m.StopAll(ctx))
		assert.Equal(t, StateStopped, tun.State())

		// Start must still be permitted (rejected only for unsupported provider,
		// NOT for shutting-down).
		_, err := m.Start(ctx, "fresh", Config{Provider: Provider("bogus"), LocalPort: 1})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "shutting down")
		assert.Contains(t, err.Error(), "unsupported tunnel provider")
	})

	t.Run("Shutdown latches shuttingDown", func(t *testing.T) {
		m := NewManager()
		tun := connectedTunnel("h1:web:8080", "/projA", "u1")
		close(tun.done)
		m.tunnels.Store("h1:web:8080", tun)

		require.NoError(t, m.Shutdown(ctx))
		assert.Equal(t, StateStopped, tun.State())

		_, err := m.Start(ctx, "fresh", Config{Provider: Provider("bogus"), LocalPort: 1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shutting down")
	})

	t.Run("StopAll ctx cancellation returns ctx error", func(t *testing.T) {
		m := NewManager()
		// Blocker tunnel never closes done.
		m.tunnels.Store("h1:web:8080", connectedTunnel("h1:web:8080", "/p", "u"))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		err := m.StopAll(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestManager_StopUnknownID(t *testing.T) {
	m := NewManager()
	err := m.Stop(context.Background(), "ghost")
	assert.ErrorIs(t, err, ErrTunnelNotFound)
	// errors.Join sanity: joining no errors is nil.
	assert.NoError(t, errors.Join())
	// strings.Split component matching sanity used by Get.
	assert.Equal(t, []string{"a", "b", "c"}, strings.Split("a:b:c", ":"))
}

// StopExact retires the stopped tunnel's own entry before it returns, so the
// caller can report the stop (or retry the id) without the stopped tunnel
// still listed; it never retires a same-id replacement, and a tunnel that
// cannot be stopped stays registered.
func TestManager_StopExact(t *testing.T) {
	t.Run("stopped tunnel is retired before return, counted down once", func(t *testing.T) {
		m := NewManager()
		tun := connectedTunnel("web", "/projA", "u1")
		close(tun.done)
		m.tunnels.Store("web", tun)
		m.active.Add(1)

		require.NoError(t, m.StopExact(context.Background(), tun))
		_, err := m.Get("web")
		assert.ErrorIs(t, err, ErrTunnelNotFound, "entry must be gone when StopExact returns")
		assert.Equal(t, 0, m.ActiveCount())

		m.retire(tun) // the Done watcher retiring it again must not double-count
		assert.Equal(t, 0, m.ActiveCount())
	})

	t.Run("same-id replacement is never retired", func(t *testing.T) {
		m := NewManager()
		old := connectedTunnel("web", "/projA", "u1")
		close(old.done)
		replacement := connectedTunnel("web", "/projA", "u2")
		m.tunnels.Store("web", replacement)
		m.active.Add(1)

		require.NoError(t, m.StopExact(context.Background(), old))
		got, err := m.Get("web")
		require.NoError(t, err)
		assert.Same(t, replacement, got)
		assert.Equal(t, 1, m.ActiveCount())
	})

	t.Run("unstoppable tunnel stays registered", func(t *testing.T) {
		m := NewManager()
		blocker := connectedTunnel("web", "/projA", "u1") // done never closes
		m.tunnels.Store("web", blocker)
		m.active.Add(1)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, m.StopExact(ctx, blocker), context.DeadlineExceeded)
		got, err := m.Get("web")
		require.NoError(t, err)
		assert.Same(t, blocker, got)
		assert.Equal(t, 1, m.ActiveCount())
	})
}

// Every stop path retires the entry it stopped before returning. Stop by id,
// StopByProjectPath, StopAll and Shutdown used to stop the tunnel and leave
// the entry to the Done watcher goroutine, so TUNNEL STOP could reply
// "stopped" while TUNNEL LIST still listed the tunnel and a START of the same
// id was refused as already existing. The entries here have no watcher, so a
// path that relies on one leaves them registered.
func TestManager_StopPathsRetireBeforeReturn(t *testing.T) {
	ctx := context.Background()
	register := func(m *Manager, id, path string) *Tunnel {
		tun := connectedTunnel(id, path, "u")
		close(tun.done)
		m.tunnels.Store(id, tun)
		m.active.Add(1)
		return tun
	}
	assertRetired := func(t *testing.T, m *Manager, ids ...string) {
		t.Helper()
		for _, id := range ids {
			_, ok := m.tunnels.Load(id)
			assert.False(t, ok, "%s still registered after its stop returned", id)
		}
		assert.Equal(t, 0, m.ActiveCount())
		assert.Empty(t, m.List())
	}

	t.Run("Stop", func(t *testing.T) {
		m := NewManager()
		register(m, "h1:web:8080", "/projA")
		require.NoError(t, m.Stop(ctx, "h1:web:8080"))
		assertRetired(t, m, "h1:web:8080")
	})
	t.Run("StopByProjectPath", func(t *testing.T) {
		m := NewManager()
		register(m, "h1:web:8080", "/projA")
		ids, err := m.StopByProjectPath(ctx, "/projA")
		require.NoError(t, err)
		assert.Equal(t, []string{"h1:web:8080"}, ids)
		assertRetired(t, m, "h1:web:8080")
	})
	t.Run("StopAll", func(t *testing.T) {
		m := NewManager()
		register(m, "h1:web:8080", "/projA")
		register(m, "h2:api:9090", "/projB")
		require.NoError(t, m.StopAll(ctx))
		assertRetired(t, m, "h1:web:8080", "h2:api:9090")
	})
	t.Run("Shutdown", func(t *testing.T) {
		m := NewManager()
		register(m, "h1:web:8080", "/projA")
		require.NoError(t, m.Shutdown(ctx))
		assertRetired(t, m, "h1:web:8080")
	})
}

// LastOutput reports the provider's latest line while it is still running,
// which is what a URL-wait timeout quotes.
func TestTunnel_LastOutputWhileRunning(t *testing.T) {
	tun := fakeProviderTunnel(t, ProviderCloudflare, "silent")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, tun.Start(ctx))
	t.Cleanup(func() { _ = tun.Stop(context.Background()) })

	require.Eventually(t, func() bool {
		return strings.Contains(tun.LastOutput(), "still connecting")
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, StateStarting, tun.State())
}

// A Start that loses the race with Shutdown must not leave a started tunnel
// behind: the flag is re-checked after the registry store.
func TestManager_StartAfterShutdownFlagRetires(t *testing.T) {
	m := NewManager()
	m.shuttingDown.Store(true)
	_, err := m.Start(context.Background(), "late", Config{Provider: ProviderCloudflare, LocalPort: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutting down")
	assert.Equal(t, 0, m.ActiveCount())
}
