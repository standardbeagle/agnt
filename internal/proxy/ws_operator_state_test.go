package proxy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The chaos engine's state is the operator's fault-injection configuration;
// chaos control is refused to untrusted connections, and its state push must
// not reach them either.
func TestChaosState_NotSentToUntrustedConnections(t *testing.T) {
	ps := newGateProxy(t)
	_, pageStub := registerConn(t, ps, "conn-page", true)
	_, visitorStub := registerConn(t, ps, "conn-visitor", false)

	ps.BroadcastChaosState()

	require.Eventually(t, func() bool { return len(framesOfType(t, pageStub, "chaos_state")) == 1 },
		3*time.Second, 5*time.Millisecond, "premise: the trusted page receives chaos_state")
	assert.Zero(t, visitorStub.count(), "an untrusted connection must not receive chaos_state")
}

// The self-signed-certificate notice is sent on connect to trusted connections
// only, in the toast shape toast.js reads: severity under payload.type. The old
// frame put "toast":"warning" at the top level, so the browser read
// payload = message and rendered a toast of type "toast" — no warning styling.
func TestTLSSkipNotice_TrustedOnlyAndWarningShaped(t *testing.T) {
	ps := newGateProxy(t)

	assert.Nil(t, ps.tlsSkipNotice(true), "no notice while TLS verification is not bypassed")

	ps.tlsCertSkipped.Store(true)
	assert.Nil(t, ps.tlsSkipNotice(false), "an untrusted connection must not be told about the operator's backend")

	frame := ps.tlsSkipNotice(true)
	require.NotNil(t, frame, "a trusted connection gets the notice")
	var msg struct {
		Type    string `json:"type"`
		Payload struct {
			Type     string `json:"type"`
			Title    string `json:"title"`
			Message  string `json:"message"`
			Duration int    `json:"duration"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(frame, &msg))
	assert.Equal(t, "toast", msg.Type)
	assert.Equal(t, "warning", msg.Payload.Type)
	assert.Equal(t, "Self-Signed Certificate", msg.Payload.Title)
	assert.NotEmpty(t, msg.Payload.Message)
	assert.Equal(t, 8000, msg.Payload.Duration)
}
