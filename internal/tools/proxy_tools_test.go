package tools

import (
	"encoding/json"
	"fmt"
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

func TestProxyLogQueryRaw_AllLogTypes(t *testing.T) {
	now := time.Now()

	// Build one entry per log type with populated data
	entries := []proxy.LogEntry{
		{Type: proxy.LogTypeInteraction, Interaction: &proxy.InteractionEvent{
			ID: "int-1", Timestamp: now, EventType: "click",
			Target: proxy.InteractionTarget{Selector: ".btn", Tag: "button"},
			URL:    "http://localhost:3000",
		}},
		{Type: proxy.LogTypeMutation, Mutation: &proxy.MutationEvent{
			ID: "mut-1", Timestamp: now, MutationType: "added",
			Target:  proxy.MutationTarget{Selector: "#list", Tag: "ul"},
			Added:   []proxy.MutationNode{{Tag: "li"}},
			Removed: []proxy.MutationNode{},
			URL:     "http://localhost:3000",
		}},
		{Type: proxy.LogTypeSketch, Sketch: &proxy.SketchEntry{
			ID: "sk-1", Timestamp: now, URL: "http://localhost:3000",
			Description: "wireframe", ElementCount: 5, FilePath: "/tmp/sketch.png",
		}},
		{Type: proxy.LogTypeScreenshotCapture, ScreenshotCapture: &proxy.ScreenshotCapture{
			ID: "sc-1", Timestamp: now, URL: "http://localhost:3000",
			Summary: "header area", FilePath: "/tmp/cap.png",
		}},
		{Type: proxy.LogTypeElementCapture, ElementCapture: &proxy.ElementCapture{
			ID: "ec-1", Timestamp: now, URL: "http://localhost:3000",
			Summary: "nav element", Selector: "nav.main", Tag: "nav",
		}},
		{Type: proxy.LogTypeSketchCapture, SketchCapture: &proxy.SketchCapture{
			ID: "skc-1", Timestamp: now, URL: "http://localhost:3000",
			Summary: "login flow", ElementCount: 3, FilePath: "/tmp/skc.png",
		}},
		{Type: proxy.LogTypeDesignState, DesignState: &proxy.DesignState{
			ID: "ds-1", Timestamp: now, Selector: ".card", XPath: "//div[@class='card']",
			OriginalHTML: "<div>card</div>", ContextHTML: "<section><div>card</div></section>",
			Metadata: proxy.DesignElementMetadata{Tag: "div"}, URL: "http://localhost:3000",
		}},
		{Type: proxy.LogTypeDesignRequest, DesignRequest: &proxy.DesignRequest{
			ID: "dr-1", Timestamp: now, Selector: ".card", XPath: "//div",
			CurrentHTML: "<div>v2</div>", OriginalHTML: "<div>card</div>",
			ContextHTML: "<section></section>", AlternativesCount: 2,
			Metadata: proxy.DesignElementMetadata{Tag: "div"}, URL: "http://localhost:3000",
		}},
		{Type: proxy.LogTypeDesignChat, DesignChat: &proxy.DesignChat{
			ID: "dc-1", Timestamp: now, Message: "make it blue",
			Selector: ".card", XPath: "//div", CurrentHTML: "<div>v2</div>",
			OriginalHTML: "<div>card</div>", ContextHTML: "<section></section>",
			Metadata: proxy.DesignElementMetadata{Tag: "div"}, URL: "http://localhost:3000",
		}},
	}

	result, output, err := handleProxyLogQueryRaw(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, len(entries))

	for i, e := range output.Entries {
		t.Run(fmt.Sprintf("raw_%s", e.Type), func(t *testing.T) {
			assert.Equal(t, string(entries[i].Type), e.Type)
			assert.NotEmpty(t, e.Data, "should not produce empty data for type %s", e.Type)
			assert.False(t, e.Timestamp.IsZero(), "should not have zero timestamp for type %s", e.Type)
		})
	}
}

func TestProxyLogQueryCompact_AllLogTypes(t *testing.T) {
	now := time.Now()

	entries := []proxy.LogEntry{
		{Type: proxy.LogTypeScreenshotCapture, ScreenshotCapture: &proxy.ScreenshotCapture{
			ID: "sc-1", Timestamp: now, Summary: "header", FilePath: "/tmp/cap.png",
		}},
		{Type: proxy.LogTypeElementCapture, ElementCapture: &proxy.ElementCapture{
			ID: "ec-1", Timestamp: now, Summary: "nav", Selector: "nav.main", Tag: "nav",
		}},
		{Type: proxy.LogTypeSketchCapture, SketchCapture: &proxy.SketchCapture{
			ID: "skc-1", Timestamp: now, Summary: "flow", ElementCount: 3,
		}},
		{Type: proxy.LogTypeDesignState, DesignState: &proxy.DesignState{
			ID: "ds-1", Timestamp: now, Selector: ".card",
			Metadata: proxy.DesignElementMetadata{Tag: "div"}, URL: "http://localhost:3000",
		}},
		{Type: proxy.LogTypeDesignRequest, DesignRequest: &proxy.DesignRequest{
			ID: "dr-1", Timestamp: now, Selector: ".card", AlternativesCount: 2,
		}},
		{Type: proxy.LogTypeDesignChat, DesignChat: &proxy.DesignChat{
			ID: "dc-1", Timestamp: now, Message: "make it blue", Selector: ".card",
		}},
	}

	result, output, err := handleProxyLogQueryCompact(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, len(entries))

	for i, e := range output.Entries {
		t.Run(fmt.Sprintf("compact_%s", e.Type), func(t *testing.T) {
			assert.Equal(t, string(entries[i].Type), e.Type)
			assert.NotEmpty(t, e.Data, "should not produce empty data for type %s", e.Type)
			assert.False(t, e.Timestamp.IsZero(), "should not have zero timestamp for type %s", e.Type)
		})
	}

	// Verify specific compact formatting
	assert.Contains(t, output.Entries[0].Data, "header")          // ScreenshotCapture summary
	assert.Contains(t, output.Entries[3].Data, "Design selected") // DesignState
	assert.Contains(t, output.Entries[4].Data, "2 existing")      // DesignRequest alternatives
	assert.Contains(t, output.Entries[5].Data, "make it blue")    // DesignChat message
}

func TestProxyLogQueryRaw_DefaultFallback(t *testing.T) {
	// Use an entry with a custom/unknown type to verify the default fallback
	entry := proxy.LogEntry{
		Type: proxy.LogEntryType("unknown_future_type"),
	}

	result, output, err := handleProxyLogQueryRaw([]proxy.LogEntry{entry}, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	assert.Equal(t, "unknown_future_type", output.Entries[0].Type)
	assert.NotEmpty(t, output.Entries[0].Data, "default fallback should produce data")
	assert.False(t, output.Entries[0].Timestamp.IsZero(), "default fallback should set timestamp")

	// Verify it's valid JSON containing the type
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(output.Entries[0].Data), &parsed))
	assert.Equal(t, "unknown_future_type", parsed["type"])
}

func TestProxyLogQueryCompact_DefaultFallback(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogEntryType("unknown_future_type"),
	}

	result, output, err := handleProxyLogQueryCompact([]proxy.LogEntry{entry}, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	assert.Equal(t, "unknown_future_type", output.Entries[0].Type)
	assert.NotEmpty(t, output.Entries[0].Data, "default fallback should produce data")
	assert.False(t, output.Entries[0].Timestamp.IsZero(), "default fallback should set timestamp")
}
