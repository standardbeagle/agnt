package tunnel

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseCase exercises a parse method directly with an io.Reader (no subprocess).
// Each parser scans lines, and on the first URL match sets the public URL,
// flips state to Connected, and fires the onURL callback.
func TestParseOutput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantURL string
		parse   func(t *Tunnel, r *strings.Reader)
	}{
		{
			name:    "cloudflare",
			input:   "INF starting\nINF | https://threaded-fathers-explore-supplier.trycloudflare.com |\nINF connected\n",
			wantURL: "https://threaded-fathers-explore-supplier.trycloudflare.com",
			parse:   func(t *Tunnel, r *strings.Reader) { t.parseCloudflareOutput(r) },
		},
		{
			name:    "ngrok",
			input:   "starting\nForwarding https://abc123def.ngrok.io -> http://localhost:8080\n",
			wantURL: "https://abc123def.ngrok.io",
			parse:   func(t *Tunnel, r *strings.Reader) { t.parseNgrokOutput(r) },
		},
		{
			name:    "tailscale",
			input:   "Available within your tailnet:\nhttps://machine.tailnet.ts.net/\n",
			wantURL: "https://machine.tailnet.ts.net/",
			parse:   func(t *Tunnel, r *strings.Reader) { t.parseTailscaleOutput(r) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tun := New(Config{Provider: ProviderCloudflare, LocalPort: 8080})
			var cbURL atomic.Pointer[string]
			tun.OnURL(func(u string) { cbURL.Store(&u) })

			// Parsers only run after Start in production; the Connected
			// transition is a CAS from Starting so a late URL line can never
			// resurrect a terminal state.
			tun.setState(StateStarting)
			tt.parse(tun, strings.NewReader(tt.input))

			assert.Equal(t, tt.wantURL, tun.PublicURL(), "public URL set from parsed line")
			assert.Equal(t, StateConnected, tun.State(), "state flips to connected")
			require.NotNil(t, cbURL.Load(), "onURL callback fired")
			assert.Equal(t, tt.wantURL, *cbURL.Load(), "callback received the URL")
		})
	}

	t.Run("late url does not resurrect terminal state", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		tun.setState(StateStopped)
		tun.parseCloudflareOutput(strings.NewReader("INF | https://late.trycloudflare.com |\n"))
		assert.Equal(t, StateStopped, tun.State(), "terminal state is not stomped")
	})

	t.Run("no url leaves idle and no callback", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		fired := false
		tun.OnURL(func(string) { fired = true })
		tun.parseCloudflareOutput(strings.NewReader("just logs\nno url here\n"))
		assert.Equal(t, "", tun.PublicURL())
		assert.Equal(t, StateIdle, tun.State())
		assert.False(t, fired)
	})
}

func TestSetPublicURL(t *testing.T) {
	t.Run("callback invoked", func(t *testing.T) {
		tun := New(Config{Provider: ProviderNgrok, LocalPort: 1})
		var got string
		var calls int
		tun.OnURL(func(u string) { got = u; calls++ })
		tun.setPublicURL("https://x.ngrok.io")
		assert.Equal(t, "https://x.ngrok.io", got)
		assert.Equal(t, 1, calls)
		assert.Equal(t, "https://x.ngrok.io", tun.PublicURL())
	})

	t.Run("nil callback is safe", func(t *testing.T) {
		tun := New(Config{Provider: ProviderNgrok, LocalPort: 1})
		assert.NotPanics(t, func() { tun.setPublicURL("https://y.ngrok.io") })
		assert.Equal(t, "https://y.ngrok.io", tun.PublicURL())
	})
}

func TestWaitForURL(t *testing.T) {
	t.Run("returns url already set via done", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		tun.setPublicURL("https://ready.trycloudflare.com")
		close(tun.done)
		url, err := tun.WaitForURL(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "https://ready.trycloudflare.com", url)
	})

	t.Run("returns url discovered via ticker poll", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		// Set the URL slightly after WaitForURL begins; the 100ms ticker picks it up.
		go func() {
			tun.setPublicURL("https://late.trycloudflare.com")
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		url, err := tun.WaitForURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://late.trycloudflare.com", url)
		assert.Equal(t, "https://late.trycloudflare.com", tun.PublicURL())
	})

	t.Run("ctx timeout returns ctx error", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		url, err := tun.WaitForURL(ctx)
		assert.Equal(t, "", url)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("done closed with error returns tunnel error", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		sentinel := errors.New("cloudflared exited boom")
		tun.setError(sentinel)
		close(tun.done)
		url, err := tun.WaitForURL(context.Background())
		assert.Equal(t, "", url)
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
	})

	t.Run("done closed clean no url returns sentinel message", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		close(tun.done)
		url, err := tun.WaitForURL(context.Background())
		assert.Equal(t, "", url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "closed without providing URL")
	})
}

func TestTunnelStart_DoubleAndUnsupported(t *testing.T) {
	t.Run("double start: second returns already started", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		// Manually CAS Idle->Starting to simulate a first Start without spawning
		// a real subprocess.
		require.True(t, tun.compareAndSwapState(StateIdle, StateStarting))
		err := tun.Start(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already started")
		assert.Equal(t, StateStarting, tun.State(), "state unchanged by rejected second start")
	})

	t.Run("unsupported provider -> StateFailed, done closed", func(t *testing.T) {
		tun := New(Config{Provider: Provider("bogus"), LocalPort: 1})
		err := tun.Start(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported tunnel provider")
		assert.Equal(t, StateFailed, tun.State())
		// done MUST close on this terminal path, otherwise Stop/WaitForURL
		// block until context expiry.
		select {
		case <-tun.done:
		default:
			t.Fatal("done must be closed for unsupported provider")
		}
		// cancel was installed before the switch.
		assert.NotNil(t, tun.cancel)
	})
}

func TestTunnelStop(t *testing.T) {
	t.Run("stop before start is a no-op", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		require.NoError(t, tun.Stop(context.Background()))
		assert.Equal(t, StateIdle, tun.State(), "idle tunnel stays idle")
	})

	t.Run("nil cmd/process safe when done already closed", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		// Simulate a started tunnel whose cmd never got published; close
		// done so the select returns immediately.
		tun.setState(StateStarting)
		close(tun.done)
		require.NoError(t, tun.Stop(context.Background()))
		assert.Equal(t, StateStopped, tun.State())
	})

	t.Run("ctx timeout when done never closes", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		tun.setState(StateStarting)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		err := tun.Stop(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		// State is not advanced to Stopped on the timeout branch.
		assert.NotEqual(t, StateStopped, tun.State())
	})

	t.Run("invokes cancel when set", func(t *testing.T) {
		tun := New(Config{Provider: ProviderCloudflare, LocalPort: 1})
		tun.setState(StateStarting)
		var cancelled atomic.Bool
		_, cancel := context.WithCancel(context.Background())
		tun.procMu.Lock()
		tun.cancel = func() { cancelled.Store(true); cancel() }
		tun.procMu.Unlock()
		close(tun.done)
		require.NoError(t, tun.Stop(context.Background()))
		assert.True(t, cancelled.Load(), "Stop must invoke the stored cancel func")
		assert.Equal(t, StateStopped, tun.State())
	})
}

func TestTunnelInfo_ErrorField(t *testing.T) {
	tun := New(Config{ID: "tid", Provider: ProviderNgrok, LocalPort: 3000, LocalHost: "127.0.0.1", Path: "/proj"})
	tun.setError(errors.New("ngrok exited: signal killed"))
	tun.setState(StateFailed)
	info := tun.Info()
	assert.Equal(t, "tid", info.ID)
	assert.Equal(t, ProviderNgrok, info.Provider)
	assert.Equal(t, "failed", info.State)
	assert.Equal(t, "127.0.0.1:3000", info.LocalAddr)
	assert.Equal(t, "/proj", info.Path)
	assert.Contains(t, info.Error, "ngrok exited")

	// No error -> empty Error field.
	clean := New(Config{ID: "c", Provider: ProviderCloudflare, LocalPort: 1})
	assert.Empty(t, clean.Info().Error)
}

// Tailscale DNS fallback (platform.TailscaleDNSName) is not covered here: it
// requires a real `tailscale status --json` invocation and there is no seam to
// stub it. Deferred — see task note. The parse-path coverage above exercises
// the URL-discovery branch of the tailscale tunnel.
