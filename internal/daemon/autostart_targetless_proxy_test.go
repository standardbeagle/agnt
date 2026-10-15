package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/standardbeagle/agnt/internal/config"
)

// A proxy declared with `autostart true` but no target and no script can never
// be created. autostartProxy returned nil for it, so autostart logged
// proxy_started and listed it in the result as running.
func TestAutostartProxies_TargetlessProxyIsAnErrorNotStarted(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	cfg := &config.AgntConfig{Proxies: map[string]*config.ProxyConfig{
		"web": {Autostart: true},
	}}

	result := &AutostartResult{}
	d.startAutostartProxies(context.Background(), cfg, dir, map[string]bool{}, result)

	assert.NotContains(t, result.Proxies, "web", "a proxy that was never created must not be reported as started")
	if assert.Len(t, result.Errors, 1) {
		assert.True(t, strings.Contains(result.Errors[0], "no target"), result.Errors[0])
	}
	assert.Empty(t, startupEvents(d, makeProcessID(dir, "web"), "proxy_started"))
	assert.NotEmpty(t, startupEvents(d, makeProcessID(dir, "web"), "proxy_failed"))
}
