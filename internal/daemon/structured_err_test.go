package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A structured daemon error (unknown sub-command, missing parameter) must
// reach the client as an error. It used to travel as an ordinary JSON
// response, so a caller checking only err read it as a successful result.
// The error text keeps the "[code] {json}" form formatDaemonError
// (internal/tools/toolhelpers.go) parses back into valid actions.
func TestStructuredErrorIsAnErrorToTheClient(t *testing.T) {
	c := newBootedClient(t)

	// Every verb the shared hub ships a built-in handler for, plus one of
	// agnt's own: none may answer an unknown action with a success frame.
	for _, verb := range []string{"PROC", "SCRIPT", "SESSION", "PROXY"} {
		t.Run(verb, func(t *testing.T) {
			res, err := c.Conn().Request(verb, "NO-SUCH-ACTION").JSON()
			require.Error(t, err, "an unknown sub-command came back as a result: %v", res)
			assert.Nil(t, res)

			idx := strings.Index(err.Error(), "] {")
			require.GreaterOrEqual(t, idx, 0, "error is not in the [code] {json} form: %v", err)
			var se hubproto.StructuredError
			require.NoError(t, json.Unmarshal([]byte(err.Error()[idx+2:]), &se))
			assert.Equal(t, verb, se.Command)
			assert.NotEmpty(t, se.ValidActions, "the valid actions must survive the trip")
		})
	}
}
