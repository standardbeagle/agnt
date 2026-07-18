package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The secret-input validation runs before any daemon connection is needed,
// so a bare DaemonTools exercises it.
func callChannelReply(t *testing.T, input ChannelReplyInput) *string {
	t.Helper()
	dt := &DaemonTools{}
	result, _, err := dt.makeChannelReplyHandler()(context.Background(), nil, input)
	require.NoError(t, err, "handler errors surface as CallToolResult, not Go errors")
	if result == nil || !result.IsError {
		return nil
	}
	require.NotEmpty(t, result.Content)
	if tc, ok := result.Content[0].(interface{ MarshalJSON() ([]byte, error) }); ok {
		data, _ := tc.MarshalJSON()
		s := string(data)
		return &s
	}
	s := ""
	return &s
}

func TestChannelReply_SecretInput_Validation(t *testing.T) {
	base := ChannelReplyInput{Content: "Paste your key"}

	// Unknown input mode rejected.
	in := base
	in.Input = "textarea"
	msg := callChannelReply(t, in)
	require.NotNil(t, msg, "unknown input mode must be rejected")
	assert.Contains(t, *msg, "only 'secret' is supported")

	// input secret without a name rejected.
	in = base
	in.Input = "secret"
	msg = callChannelReply(t, in)
	require.NotNil(t, msg, "input secret without name must be rejected")
	assert.Contains(t, *msg, "env-var-style name")

	// input secret with a non-env-safe name rejected.
	in.Name = "not a name"
	msg = callChannelReply(t, in)
	require.NotNil(t, msg)
	assert.Contains(t, *msg, "env-var-style name")

	// Valid secret request passes validation and proceeds to the daemon
	// connection step (which fails here — no daemon — but NOT with a
	// validation message).
	in.Name = "FIGMA_KEY"
	msg = callChannelReply(t, in)
	if msg != nil {
		assert.NotContains(t, *msg, "env-var-style name")
		assert.NotContains(t, *msg, "only 'secret' is supported")
	}
}
