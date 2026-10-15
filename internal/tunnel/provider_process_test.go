package tunnel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProviderEnv selects a fake tunnel-provider behaviour for a re-exec of
// this test binary. The test binary stands in for cloudflared/ngrok so the
// real Start → launch → Wait path runs on every OS.
const fakeProviderEnv = "AGNT_TUNNEL_FAKE_PROVIDER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeProviderEnv); mode != "" {
		os.Exit(runFakeProvider(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runFakeProvider(mode string, args []string) int {
	switch mode {
	case "ngrok":
		// Real ngrok with its output on a pipe runs no console UI and logs
		// nothing unless asked to (--log); model exactly that.
		for _, a := range args {
			if a == "--log=stdout" {
				fmt.Println(`t=2026-10-14T10:00:00+0000 lvl=info msg="client session established" obj=tunnels.session`)
				fmt.Println(`t=2026-10-14T10:00:00+0000 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://localhost:3000 url=https://fake-1234.ngrok-free.dev`)
			}
		}
		time.Sleep(30 * time.Second)
		return 0
	case "die":
		for i := 0; i < 200; i++ {
			fmt.Fprintf(os.Stderr, "INF noise line %d\n", i)
		}
		fmt.Fprintln(os.Stderr, "ERR failed to request quick Tunnel: 429 Too Many Requests")
		return 1
	case "exit0":
		return 0
	case "silent":
		// Runs, logs, and never prints a public URL.
		fmt.Fprintln(os.Stderr, "INF still connecting to the edge")
		time.Sleep(30 * time.Second)
		return 0
	}
	return 2
}

func fakeProviderTunnel(t *testing.T, provider Provider, mode string) *Tunnel {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	t.Setenv(fakeProviderEnv, mode)
	return New(Config{Provider: provider, LocalHost: "127.0.0.1", LocalPort: 3000, BinaryPath: self})
}

// An ngrok tunnel must surface its public URL. ngrok prints nothing on a pipe
// unless told to log, and the free domain current accounts get is
// .ngrok-free.dev; before the fix neither held, so WaitForURL never resolved.
func TestNgrokStart_ReadsURLFromLogOutput(t *testing.T) {
	tun := fakeProviderTunnel(t, ProviderNgrok, "ngrok")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, tun.Start(ctx))
	t.Cleanup(func() { _ = tun.Stop(context.Background()) })

	url, err := tun.WaitForURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://fake-1234.ngrok-free.dev", url)
	assert.Equal(t, StateConnected, tun.State())

	// A requested stop is not a failure.
	require.NoError(t, tun.Stop(ctx))
	assert.Equal(t, StateStopped, tun.State())
	assert.Empty(t, tun.Info().Error, "Stop must not record an error")
}

// A provider that dies says why in its last output line; the tunnel's error
// must carry that line, not just "exit status 1".
func TestProviderExit_ErrorQuotesLastOutputLine(t *testing.T) {
	tun := fakeProviderTunnel(t, ProviderCloudflare, "die")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, tun.Start(ctx))

	_, err := tun.WaitForURL(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloudflared exited")
	assert.Contains(t, err.Error(), "429 Too Many Requests", "the reason the provider printed is in the error")
	assert.Equal(t, StateFailed, tun.State())
	assert.Contains(t, tun.Info().Error, "429 Too Many Requests")
}

// A provider that exits on its own with status 0 has stopped tunnelling all
// the same: the tunnel must report failed, not stay "starting".
func TestProviderExit_CleanUnrequestedExitIsAFailure(t *testing.T) {
	tun := fakeProviderTunnel(t, ProviderCloudflare, "exit0")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, tun.Start(ctx))

	select {
	case <-tun.Done():
	case <-ctx.Done():
		t.Fatal("tunnel did not finish after its process exited")
	}
	assert.Equal(t, StateFailed, tun.State())
	assert.True(t, strings.Contains(tun.Info().Error, "exited unexpectedly"), "error = %q", tun.Info().Error)
}

func TestNgrokArgs_LogToStdout(t *testing.T) {
	assert.Equal(t, []string{"http", "--log=stdout", "--log-format=logfmt", "3000"}, ngrokArgs("localhost", 3000))
	assert.Equal(t, []string{"http", "--log=stdout", "--log-format=logfmt", "10.0.0.5:3000"}, ngrokArgs("10.0.0.5", 3000))
}
