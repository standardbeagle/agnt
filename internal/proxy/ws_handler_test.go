package proxy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTLSSkipWarningToastJSON_MatchesBroadcastToastShape pins the fix for
// the TLS-skip warning sent to a freshly connected browser client: it used
// to be a flat {type:"toast", toast:"warning", ...}, but toast.js reads
// message.payload.type (see ws_broadcast.go BroadcastToastOpts), so the
// flat shape was silently ignored by every client.
func TestTLSSkipWarningToastJSON_MatchesBroadcastToastShape(t *testing.T) {
	raw, err := tlsSkipWarningToastJSON()
	require.NoError(t, err)

	var msg struct {
		Type    string `json:"type"`
		Toast   string `json:"toast"` // must NOT be present (the old, wrong shape)
		Payload struct {
			Type     string `json:"type"`
			Title    string `json:"title"`
			Message  string `json:"message"`
			Duration int    `json:"duration"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(raw, &msg))

	assert.Equal(t, "toast", msg.Type)
	assert.Empty(t, msg.Toast, "must not use the flat message.toast shape toast.js does not read")
	assert.Equal(t, "warning", msg.Payload.Type)
	assert.NotEmpty(t, msg.Payload.Title)
	assert.Contains(t, msg.Payload.Message, "self-signed", "message should mention the cert issue (case-insensitive substring check below)")
	assert.Positive(t, msg.Payload.Duration)

	// Mirror BroadcastToastOpts's own wire shape exactly (same two top-level
	// keys, payload carries everything else).
	var generic map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &generic))
	_, hasPayload := generic["payload"].(map[string]interface{})
	assert.True(t, hasPayload)
	assert.Len(t, generic, 2, "only type + payload at the top level, like BroadcastToastOpts")
}
