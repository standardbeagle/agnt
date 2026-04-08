package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for all WebSocket message parse functions.
// These cover the browser→proxy leg of the message pipeline.

var testTS = time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
var testURL = "http://localhost:3000/dashboard"

// ── parseInteractionEvent ──────────────────────────────────────────────────

func TestParseInteractionEvent_Click(t *testing.T) {
	data := map[string]interface{}{
		"event_type": "click",
		"target": map[string]interface{}{
			"selector": ".btn-primary",
			"tag":      "button",
			"id":       "submit-btn",
			"text":     "Submit",
			"classes":  []interface{}{"btn", "btn-primary"},
			"attributes": map[string]interface{}{
				"type": "submit",
			},
		},
		"position": map[string]interface{}{
			"client_x": float64(120),
			"client_y": float64(45),
			"page_x":   float64(120),
			"page_y":   float64(345),
		},
	}

	event := parseInteractionEvent(data, "metric-1", testTS, testURL)

	assert.Equal(t, "click", event.EventType)
	assert.Equal(t, ".btn-primary", event.Target.Selector)
	assert.Equal(t, "button", event.Target.Tag)
	assert.Equal(t, "submit-btn", event.Target.ID)
	assert.Equal(t, "Submit", event.Target.Text)
	assert.Equal(t, []string{"btn", "btn-primary"}, event.Target.Classes)
	assert.Equal(t, "submit", event.Target.Attributes["type"])
	require.NotNil(t, event.Position)
	assert.Equal(t, 120, event.Position.ClientX)
	assert.Equal(t, 45, event.Position.ClientY)
	assert.Equal(t, testURL, event.URL)
	assert.Nil(t, event.Key)
}

func TestParseInteractionEvent_Keyboard(t *testing.T) {
	data := map[string]interface{}{
		"event_type": "keydown",
		"target": map[string]interface{}{
			"selector": "input#search",
			"tag":      "input",
		},
		"key": map[string]interface{}{
			"key":   "Enter",
			"code":  "Enter",
			"ctrl":  false,
			"shift": false,
		},
	}

	event := parseInteractionEvent(data, "metric-2", testTS, testURL)

	assert.Equal(t, "keydown", event.EventType)
	require.NotNil(t, event.Key)
	assert.Equal(t, "Enter", event.Key.Key)
	assert.Equal(t, "Enter", event.Key.Code)
	assert.False(t, event.Key.Ctrl)
	assert.Nil(t, event.Position)
}

func TestParseInteractionEvent_InputValue(t *testing.T) {
	data := map[string]interface{}{
		"event_type": "input",
		"target": map[string]interface{}{
			"selector": "input#email",
			"tag":      "input",
		},
		"value": "user@example.com",
	}

	event := parseInteractionEvent(data, "metric-3", testTS, testURL)

	assert.Equal(t, "input", event.EventType)
	assert.Equal(t, "user@example.com", event.Value)
}

func TestParseInteractionEvent_EmptyData(t *testing.T) {
	event := parseInteractionEvent(map[string]interface{}{}, "metric-4", testTS, testURL)

	assert.Empty(t, event.EventType)
	assert.Nil(t, event.Position)
	assert.Nil(t, event.Key)
	assert.Equal(t, testURL, event.URL)
}

// ── parseMutationEvent ──────────────────────────────────────────────────────

func TestParseMutationEvent_NodeAdded(t *testing.T) {
	data := map[string]interface{}{
		"mutation_type": "added",
		"target": map[string]interface{}{
			"selector": "#container",
			"tag":      "div",
			"id":       "container",
		},
		"added": []interface{}{
			map[string]interface{}{
				"selector": "div.new-item",
				"tag":      "div",
				"id":       "",
				"html":     "<div class=\"new-item\">New</div>",
			},
		},
	}

	event := parseMutationEvent(data, "metric-5", testTS, testURL)

	assert.Equal(t, "added", event.MutationType)
	assert.Equal(t, "#container", event.Target.Selector)
	assert.Equal(t, "div", event.Target.Tag)
	require.Len(t, event.Added, 1)
	assert.Equal(t, "div.new-item", event.Added[0].Selector)
	assert.Empty(t, event.Removed)
	assert.Nil(t, event.Attribute)
}

func TestParseMutationEvent_AttributeChange(t *testing.T) {
	data := map[string]interface{}{
		"mutation_type": "attributes",
		"target": map[string]interface{}{
			"selector": ".card",
			"tag":      "div",
		},
		"attribute": map[string]interface{}{
			"name":      "class",
			"old_value": "card",
			"new_value": "card active",
		},
	}

	event := parseMutationEvent(data, "metric-6", testTS, testURL)

	assert.Equal(t, "attributes", event.MutationType)
	require.NotNil(t, event.Attribute)
	assert.Equal(t, "class", event.Attribute.Name)
	assert.Equal(t, "card", event.Attribute.OldValue)
	assert.Equal(t, "card active", event.Attribute.NewValue)
}

func TestParseMutationEvent_NodeRemoved(t *testing.T) {
	data := map[string]interface{}{
		"mutation_type": "removed",
		"target": map[string]interface{}{
			"selector": "ul.list",
			"tag":      "ul",
		},
		"removed": []interface{}{
			map[string]interface{}{
				"tag":  "li",
				"html": "<li>Deleted item</li>",
			},
		},
	}

	event := parseMutationEvent(data, "metric-7", testTS, testURL)

	assert.Equal(t, "removed", event.MutationType)
	require.Len(t, event.Removed, 1)
	assert.Equal(t, "li", event.Removed[0].Tag)
	assert.Empty(t, event.Added)
}

// ── parsePanelMessage ───────────────────────────────────────────────────────

func TestParsePanelMessage_TextOnly(t *testing.T) {
	data := map[string]interface{}{
		"payload": map[string]interface{}{
			"message":              "Fix the button alignment",
			"request_notification": true,
		},
	}

	msg := parsePanelMessage(data, "metric-8", testTS, testURL)

	assert.Equal(t, "Fix the button alignment", msg.Message)
	assert.True(t, msg.RequestNotification)
	assert.Empty(t, msg.Attachments)
	assert.Equal(t, testURL, msg.URL)
}

func TestParsePanelMessage_WithElementAttachment(t *testing.T) {
	data := map[string]interface{}{
		"payload": map[string]interface{}{
			"message": "Look at this element",
			"attachments": []interface{}{
				map[string]interface{}{
					"type":     "element",
					"id":       "ctx_abc123",
					"selector": ".premium-card",
					"tag":      "div",
					"text":     "Card content",
					"classes":  []interface{}{"premium-card", "active"},
					"summary":  "Premium card component",
				},
			},
		},
	}

	msg := parsePanelMessage(data, "metric-9", testTS, testURL)

	assert.Equal(t, "Look at this element", msg.Message)
	require.Len(t, msg.Attachments, 1)
	att := msg.Attachments[0]
	assert.Equal(t, "element", att.Type)
	assert.Equal(t, "ctx_abc123", att.ID)
	assert.Equal(t, ".premium-card", att.Selector)
	assert.Equal(t, "div", att.Tag)
	assert.Equal(t, "Card content", att.Text)
	assert.Equal(t, []string{"premium-card", "active"}, att.Classes)
	assert.Equal(t, "Premium card component", att.Summary)
}

func TestParsePanelMessage_WithScreenshotAttachment(t *testing.T) {
	data := map[string]interface{}{
		"payload": map[string]interface{}{
			"message": "Screenshot attached",
			"attachments": []interface{}{
				map[string]interface{}{
					"type":    "screenshot",
					"id":      "ctx_ss1",
					"summary": "Full page capture",
					"area": map[string]interface{}{
						"x":      float64(0),
						"y":      float64(0),
						"width":  float64(1440),
						"height": float64(900),
					},
				},
			},
		},
	}

	msg := parsePanelMessage(data, "metric-10", testTS, testURL)

	require.Len(t, msg.Attachments, 1)
	att := msg.Attachments[0]
	assert.Equal(t, "screenshot", att.Type)
	assert.Equal(t, "ctx_ss1", att.ID)
	require.NotNil(t, att.Area)
	assert.Equal(t, 1440, att.Area.Width)
	assert.Equal(t, 900, att.Area.Height)
}

func TestParsePanelMessage_UsesReferencesAsAttachmentsFallback(t *testing.T) {
	// JS sends "references" not "attachments" — the parser should accept both
	data := map[string]interface{}{
		"payload": map[string]interface{}{
			"message": "Check this",
			"references": []interface{}{
				map[string]interface{}{
					"type":     "element",
					"id":       "ctx_ref1",
					"selector": "h1",
					"tag":      "h1",
				},
			},
		},
	}

	msg := parsePanelMessage(data, "metric-11", testTS, testURL)

	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, "ctx_ref1", msg.Attachments[0].ID)
}

// ── parseSketchEntry ────────────────────────────────────────────────────────

func TestParseSketchEntry_Full(t *testing.T) {
	data := map[string]interface{}{
		"description":   "Mobile card layout",
		"element_count": float64(5),
		"image":         "data:image/png;base64,abc123",
		"sketch": map[string]interface{}{
			"version":  "1",
			"elements": []interface{}{},
		},
	}

	entry := parseSketchEntry(data, "metric-12", testTS, testURL)

	assert.Equal(t, "Mobile card layout", entry.Description)
	assert.Equal(t, 5, entry.ElementCount)
	assert.Equal(t, "data:image/png;base64,abc123", entry.ImageData)
	assert.NotNil(t, entry.Sketch)
	assert.Equal(t, testURL, entry.URL)
}

// ── parseElementCapture ─────────────────────────────────────────────────────

func TestParseElementCapture_Full(t *testing.T) {
	data := map[string]interface{}{
		"id": "ctx_el1",
		"data": map[string]interface{}{
			"summary":  "Submit button",
			"selector": "button#submit",
			"tag":      "button",
			"id":       "submit",
			"text":     "Submit",
			"classes":  []interface{}{"btn", "primary"},
			"rect": map[string]interface{}{
				"x":      float64(100),
				"y":      float64(50),
				"width":  float64(80),
				"height": float64(40),
			},
		},
	}

	capture := parseElementCapture(data, testTS, testURL)

	assert.Equal(t, "ctx_el1", capture.ID)
	assert.Equal(t, "Submit button", capture.Summary)
	assert.Equal(t, "button#submit", capture.Selector)
	assert.Equal(t, "button", capture.Tag)
	assert.Equal(t, "submit", capture.ElementID)
	assert.Equal(t, "Submit", capture.Text)
	assert.Equal(t, []string{"btn", "primary"}, capture.Classes)
	assert.Equal(t, 80.0, capture.Rect.Width)
	assert.Equal(t, 40.0, capture.Rect.Height)
}

func TestParseElementCapture_MissingData(t *testing.T) {
	data := map[string]interface{}{
		"id": "ctx_el2",
		// no "data" sub-field
	}

	capture := parseElementCapture(data, testTS, testURL)

	assert.Equal(t, "ctx_el2", capture.ID)
	assert.Empty(t, capture.Selector)
	assert.Empty(t, capture.Classes)
	assert.Equal(t, testURL, capture.URL)
}

// ── parseSketchCapture ──────────────────────────────────────────────────────

func TestParseSketchCapture_Full(t *testing.T) {
	data := map[string]interface{}{
		"id": "ctx_sk1",
		"data": map[string]interface{}{
			"elementCount": float64(8),
			"image":        "data:image/png;base64,sketchdata",
			"sketch": map[string]interface{}{
				"elements": []interface{}{},
			},
		},
	}

	capture := parseSketchCapture(data, testTS, testURL)

	assert.Equal(t, "ctx_sk1", capture.ID)
	assert.Equal(t, 8, capture.ElementCount)
	assert.Equal(t, "Sketch with 8 elements", capture.Summary)
	assert.Equal(t, "data:image/png;base64,sketchdata", capture.ImageData)
	assert.NotNil(t, capture.Sketch)
}

// ── parseDesignState ────────────────────────────────────────────────────────

func TestParseDesignState_Full(t *testing.T) {
	data := map[string]interface{}{
		"selector":     ".premium-card",
		"xpath":        "//*[@class='premium-card']",
		"originalHTML": "<div class=\"premium-card\">Original</div>",
		"contextHTML":  "<main><div class=\"premium-card\">Original</div></main>",
		"metadata": map[string]interface{}{
			"tag":     "div",
			"id":      "card-1",
			"text":    "Card content",
			"classes": []interface{}{"premium-card", "featured"},
		},
	}

	state := parseDesignState(data, "metric-13", testTS, testURL)

	assert.Equal(t, ".premium-card", state.Selector)
	assert.Equal(t, "//*[@class='premium-card']", state.XPath)
	assert.Equal(t, "<div class=\"premium-card\">Original</div>", state.OriginalHTML)
	assert.Equal(t, "div", state.Metadata.Tag)
	assert.Equal(t, "card-1", state.Metadata.ID)
	assert.Equal(t, "Card content", state.Metadata.Text)
	assert.Equal(t, []string{"premium-card", "featured"}, state.Metadata.Classes)
	assert.Equal(t, testURL, state.URL)
}

// ── parseDesignRequest ──────────────────────────────────────────────────────

func TestParseDesignRequest_WithChatHistory(t *testing.T) {
	data := map[string]interface{}{
		"selector":          ".button",
		"currentHTML":       "<button class=\"btn old\">Click</button>",
		"originalHTML":      "<button class=\"btn\">Click</button>",
		"alternativesCount": float64(2),
		"metadata": map[string]interface{}{
			"tag": "button",
		},
		"chatHistory": []interface{}{
			map[string]interface{}{
				"timestamp": float64(1743849600000),
				"message":   "Make it bigger",
				"role":      "user",
			},
			map[string]interface{}{
				"timestamp": float64(1743849601000),
				"message":   "Done",
				"role":      "assistant",
			},
		},
	}

	req := parseDesignRequest(data, "metric-14", testTS, testURL)

	assert.Equal(t, ".button", req.Selector)
	assert.Equal(t, "<button class=\"btn old\">Click</button>", req.CurrentHTML)
	assert.Equal(t, 2, req.AlternativesCount)
	require.Len(t, req.ChatHistory, 2)
	assert.Equal(t, "Make it bigger", req.ChatHistory[0].Message)
	assert.Equal(t, "user", req.ChatHistory[0].Role)
	assert.Equal(t, "Done", req.ChatHistory[1].Message)
	assert.Equal(t, "assistant", req.ChatHistory[1].Role)
}

// ── parseDesignChat ─────────────────────────────────────────────────────────

func TestParseDesignChat_Full(t *testing.T) {
	data := map[string]interface{}{
		"message":      "Make the border radius larger",
		"selector":     ".card",
		"currentHTML":  "<div class=\"card\">Content</div>",
		"originalHTML": "<div class=\"card\">Content</div>",
		"contextHTML":  "<main><div class=\"card\">Content</div></main>",
		"metadata": map[string]interface{}{
			"tag": "div",
		},
		"chatHistory": []interface{}{
			map[string]interface{}{
				"timestamp": float64(1743849600000),
				"message":   "Fix styling",
				"role":      "user",
			},
		},
	}

	chat := parseDesignChat(data, "metric-15", testTS, testURL)

	assert.Equal(t, "Make the border radius larger", chat.Message)
	assert.Equal(t, ".card", chat.Selector)
	assert.Equal(t, "<div class=\"card\">Content</div>", chat.CurrentHTML)
	require.Len(t, chat.ChatHistory, 1)
	assert.Equal(t, "Fix styling", chat.ChatHistory[0].Message)
}

// ── getStringField / getIntField helpers ────────────────────────────────────

func TestGetStringField_Present(t *testing.T) {
	m := map[string]interface{}{"key": "value"}
	assert.Equal(t, "value", getStringField(m, "key"))
}

func TestGetStringField_Missing(t *testing.T) {
	assert.Equal(t, "", getStringField(map[string]interface{}{}, "key"))
}

func TestGetIntField_Present(t *testing.T) {
	m := map[string]interface{}{"n": float64(42)}
	assert.Equal(t, 42, getIntField(m, "n"))
}

func TestGetIntField_Missing(t *testing.T) {
	assert.Equal(t, 0, getIntField(map[string]interface{}{}, "n"))
}

func TestGetBoolField_Present(t *testing.T) {
	m := map[string]interface{}{"flag": true}
	assert.True(t, getBoolField(m, "flag"))
}

func TestGetBoolField_Missing(t *testing.T) {
	assert.False(t, getBoolField(map[string]interface{}{}, "flag"))
}
