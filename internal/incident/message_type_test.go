package incident

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewIncidentEvent_DefaultsToErrorType(t *testing.T) {
	ev := NewIncidentEvent(
		SourceHTTP5xx, SeverityError, "500",
		"GET /api/x → 500", Context{ProxyID: "dev", URL: "/api/x"}, nil,
	)
	assert.Equal(t, MessageError, ev.Type, "events default to the error lane")
}

func TestMessageType_Constants(t *testing.T) {
	assert.Equal(t, MessageType("error"), MessageError)
	assert.Equal(t, MessageType("drawing"), MessageDrawing)
	assert.Equal(t, MessageType("comment"), MessageComment)
}
