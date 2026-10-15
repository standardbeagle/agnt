package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TUNNEL LIST replies {"tunnels": [...]} with no count (hub_tunnel.go), and
// the tool read a "count" key, so it reported count 0 beside a non-empty
// list. The count must be of the tunnels returned.
func TestTunnelListOutput_CountsReturnedTunnels(t *testing.T) {
	out := tunnelListOutput(map[string]interface{}{
		"tunnels": []interface{}{
			map[string]interface{}{"id": "dev", "provider": "cloudflare", "state": "running", "public_url": "https://a.trycloudflare.com"},
			map[string]interface{}{"id": "api", "provider": "ngrok", "state": "starting"},
		},
	})
	require.Len(t, out.Tunnels, 2)
	assert.Equal(t, 2, out.Count)
	assert.Equal(t, "https://a.trycloudflare.com", out.Tunnels[0].PublicURL)

	empty := tunnelListOutput(map[string]interface{}{"tunnels": []interface{}{}})
	assert.Equal(t, 0, empty.Count)
	assert.NotNil(t, empty.Tunnels, "an empty list is [] not null")
}
