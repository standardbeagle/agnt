package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// TestProxyRuntimeStatus verifies the state-string helper used by
// PROXY STATUS / PROXY LIST responses. The AI agent relies on this
// vocabulary to decide whether 503s from the proxy represent gate
// responses or genuine upstream errors.
func TestProxyRuntimeStatus(t *testing.T) {
	tests := []struct {
		name  string
		stats proxy.ProxyStats
		want  string
	}{
		{
			name:  "stopped",
			stats: proxy.ProxyStats{Running: false, ReadyForForwarding: true},
			want:  "stopped",
		},
		{
			name:  "running with open gate",
			stats: proxy.ProxyStats{Running: true, ReadyForForwarding: true},
			want:  "running",
		},
		{
			name: "waiting for dependencies",
			stats: proxy.ProxyStats{
				Running:            true,
				ReadyForForwarding: false,
				WaitingFor:         []string{"dev-backend"},
			},
			want: "waiting_for_dependencies",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, proxyRuntimeStatus(tc.stats))
		})
	}
}
