package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/standardbeagle/agnt/internal/hookrules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hook-derived toast and tool-event text is capped by length; the cut must
// land on a rune boundary, never inside a multi-byte character.
func TestHookText_TruncatesOnRuneBoundaries(t *testing.T) {
	// "é" is 2 bytes: any odd byte cut splits it.
	long := strings.Repeat("é", 400)

	msg := buildBashRedirectMessage(&hookrules.Decision{Replacement: "proc runs", Reason: long})
	assert.True(t, utf8.ValidString(msg), "bash-redirect toast is invalid UTF-8: %q", msg[len(msg)-8:])
	assert.True(t, strings.HasSuffix(msg, "..."))
	assert.Equal(t, 300, utf8.RuneCountInString(msg))

	payload, err := json.Marshal(map[string]interface{}{
		"tool_name":  "Bash",
		"tool_input": map[string]string{"command": long},
	})
	require.NoError(t, err)
	_, _, detail, ok := buildToolEvent("pre-tool-use", payload)
	require.True(t, ok)
	assert.True(t, utf8.ValidString(detail), "tool-event detail is invalid UTF-8")
	assert.Equal(t, toolEventDetailMax, utf8.RuneCountInString(detail))

	assert.Equal(t, "héllo", truncateHookText("héllo", 5), "text at the cap is unchanged")
	assert.Equal(t, "hé...", truncateHookText("héllo!", 5))
}
