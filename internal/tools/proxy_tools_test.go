package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyLogQueryRaw_PanelMessage(t *testing.T) {
	logger := proxy.NewTrafficLogger(100)
	logger.LogPanelMessage(proxy.PanelMessage{
		ID:        "msg-1",
		Timestamp: time.Now(),
		Message:   "please update the card styles",
		URL:       "http://localhost:5173/dashboard",
		Attachments: []proxy.PanelAttachment{{
			Type:     "element",
			Selector: ".card",
			Tag:      "div",
			Summary:  "card component",
			Data:     map[string]interface{}{"classes": []interface{}{"card", "shadow-md"}},
		}},
		RequestNotification: true,
	})

	entries := logger.Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypePanelMessage}})
	require.Len(t, entries, 1)

	result, output, err := handleProxyLogQueryRaw(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	entry := output.Entries[0]
	assert.Equal(t, "panel_message", entry.Type)
	assert.False(t, entry.Timestamp.IsZero(), "timestamp should not be zero")
	assert.Contains(t, entry.Data, "please update the card styles")
	assert.Contains(t, entry.Data, "element")
	assert.Contains(t, entry.Data, ".card")
}

func TestProxyLogQueryRaw_PanelMessageNoAttachments(t *testing.T) {
	logger := proxy.NewTrafficLogger(100)
	logger.LogPanelMessage(proxy.PanelMessage{
		ID:        "msg-2",
		Timestamp: time.Now(),
		Message:   "simple message",
		URL:       "http://localhost:3000",
	})

	entries := logger.Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypePanelMessage}})
	require.Len(t, entries, 1)

	result, output, err := handleProxyLogQueryRaw(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	entry := output.Entries[0]
	assert.Equal(t, "panel_message", entry.Type)
	assert.Contains(t, entry.Data, "simple message")

	// Verify JSON parses correctly and has expected fields
	var data map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(entry.Data), &data))
	assert.Equal(t, "msg-2", data["id"])
	assert.Equal(t, "simple message", data["message"])
	assert.Equal(t, "http://localhost:3000", data["url"])
	assert.Nil(t, data["attachments"], "no attachments should be omitted")
}

func TestProxyLogQueryCompact_PanelMessage(t *testing.T) {
	logger := proxy.NewTrafficLogger(100)
	logger.LogPanelMessage(proxy.PanelMessage{
		ID:        "msg-3",
		Timestamp: time.Now(),
		Message:   "fix the sidebar",
		URL:       "http://localhost:5173/settings",
		Attachments: []proxy.PanelAttachment{
			{
				Type:     "element",
				Selector: ".sidebar",
				Summary:  "sidebar nav",
			},
			{
				Type:     "screenshot_area",
				FilePath: "/tmp/capture.png",
			},
		},
	})

	entries := logger.Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypePanelMessage}})
	require.Len(t, entries, 1)

	result, output, err := handleProxyLogQueryCompact(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	entry := output.Entries[0]
	assert.Equal(t, "panel_message", entry.Type)
	assert.Contains(t, entry.Data, "fix the sidebar")
	assert.Contains(t, entry.Data, "2 attachments")
	assert.Contains(t, entry.Data, "element:.sidebar")
	assert.Contains(t, entry.Data, "sidebar nav")
	assert.Contains(t, entry.Data, "screenshot_area")
	assert.Contains(t, entry.Data, "/tmp/capture.png")
	assert.True(t, strings.Contains(entry.Data, "page: http://localhost:5173/settings"))
}
