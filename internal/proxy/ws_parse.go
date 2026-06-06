package proxy

import (
	"encoding/json"
	"fmt"
	"time"
)

// Helper functions for extracting fields from JSON data

func getStringField(data map[string]interface{}, key string) string {
	if v, ok := data[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntField(data map[string]interface{}, key string) int {
	if v, ok := data[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return 0
}

func getInt64Field(data map[string]interface{}, key string) int64 {
	if v, ok := data[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case int:
			return int64(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return i
			}
		}
	}
	return 0
}

func getFloatField(data map[string]interface{}, key string) float64 {
	if v, ok := data[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		case json.Number:
			if f, err := n.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}

// getArrayField extracts an array from a map field.
func getArrayField(data map[string]interface{}, key string) []interface{} {
	if v, ok := data[key]; ok {
		if arr, ok := v.([]interface{}); ok {
			return arr
		}
	}
	return nil
}

// getBoolField extracts a boolean from a map field.
func getBoolField(data map[string]interface{}, key string) bool {
	if v, ok := data[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// getMapField extracts a map from a map field.
func getMapField(data map[string]interface{}, key string) map[string]interface{} {
	if v, ok := data[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	return nil
}

// isLocalhost checks if a host refers to localhost.
// This includes "localhost", "127.0.0.1", and "::1" (IPv6 loopback).
func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// parseInteractionEvent parses an interaction event from JSON data.
func parseInteractionEvent(data map[string]interface{}, id string, timestamp time.Time, url string) InteractionEvent {
	event := InteractionEvent{
		ID:        id,
		Timestamp: timestamp,
		EventType: getStringField(data, "event_type"),
		URL:       url,
	}

	// Parse target info
	if targetData, ok := data["target"].(map[string]interface{}); ok {
		event.Target = InteractionTarget{
			Selector: getStringField(targetData, "selector"),
			Tag:      getStringField(targetData, "tag"),
			ID:       getStringField(targetData, "id"),
			Text:     getStringField(targetData, "text"),
		}

		// Parse classes
		if classes, ok := targetData["classes"].([]interface{}); ok {
			for _, c := range classes {
				if s, ok := c.(string); ok {
					event.Target.Classes = append(event.Target.Classes, s)
				}
			}
		}

		// Parse attributes
		if attrs, ok := targetData["attributes"].(map[string]interface{}); ok {
			event.Target.Attributes = make(map[string]string)
			for k, v := range attrs {
				if s, ok := v.(string); ok {
					event.Target.Attributes[k] = s
				}
			}
		}
	}

	// Parse position
	if posData, ok := data["position"].(map[string]interface{}); ok {
		event.Position = &InteractionPosition{
			ClientX: getIntField(posData, "client_x"),
			ClientY: getIntField(posData, "client_y"),
			PageX:   getIntField(posData, "page_x"),
			PageY:   getIntField(posData, "page_y"),
		}
	}

	// Parse keyboard info
	if keyData, ok := data["key"].(map[string]interface{}); ok {
		event.Key = &KeyboardInfo{
			Key:   getStringField(keyData, "key"),
			Code:  getStringField(keyData, "code"),
			Ctrl:  getBoolField(keyData, "ctrl"),
			Alt:   getBoolField(keyData, "alt"),
			Shift: getBoolField(keyData, "shift"),
			Meta:  getBoolField(keyData, "meta"),
		}
	}

	// Parse value (for input events)
	event.Value = getStringField(data, "value")

	// Parse extra data
	if extraData, ok := data["data"].(map[string]interface{}); ok {
		event.Data = extraData
	}

	return event
}

// parseMutationEvent parses a mutation event from JSON data.
func parseMutationEvent(data map[string]interface{}, id string, timestamp time.Time, url string) MutationEvent {
	event := MutationEvent{
		ID:           id,
		Timestamp:    timestamp,
		MutationType: getStringField(data, "mutation_type"),
		URL:          url,
	}

	// Parse target info
	if targetData, ok := data["target"].(map[string]interface{}); ok {
		event.Target = MutationTarget{
			Selector: getStringField(targetData, "selector"),
			Tag:      getStringField(targetData, "tag"),
			ID:       getStringField(targetData, "id"),
		}
	}

	// Parse added nodes
	if added, ok := data["added"].([]interface{}); ok {
		for _, nodeData := range added {
			if nm, ok := nodeData.(map[string]interface{}); ok {
				event.Added = append(event.Added, MutationNode{
					Selector: getStringField(nm, "selector"),
					Tag:      getStringField(nm, "tag"),
					ID:       getStringField(nm, "id"),
					HTML:     getStringField(nm, "html"),
				})
			}
		}
	}

	// Parse removed nodes
	if removed, ok := data["removed"].([]interface{}); ok {
		for _, nodeData := range removed {
			if nm, ok := nodeData.(map[string]interface{}); ok {
				event.Removed = append(event.Removed, MutationNode{
					Selector: getStringField(nm, "selector"),
					Tag:      getStringField(nm, "tag"),
					ID:       getStringField(nm, "id"),
					HTML:     getStringField(nm, "html"),
				})
			}
		}
	}

	// Parse attribute change
	if attrData, ok := data["attribute"].(map[string]interface{}); ok {
		event.Attribute = &AttributeChange{
			Name:     getStringField(attrData, "name"),
			OldValue: getStringField(attrData, "old_value"),
			NewValue: getStringField(attrData, "new_value"),
		}
	}

	return event
}

// parsePanelMessage parses a panel message from JSON data.
func parsePanelMessage(data map[string]interface{}, id string, timestamp time.Time, url string) PanelMessage {
	msg := PanelMessage{
		ID:        id,
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse payload
	if payload, ok := data["payload"].(map[string]interface{}); ok {
		msg.Message = getStringField(payload, "message")
		msg.RequestNotification = getBoolField(payload, "request_notification")

		// Parse attachments - check both "attachments" and "references" (JS uses "references")
		attachments, ok := payload["attachments"].([]interface{})
		if !ok {
			attachments, ok = payload["references"].([]interface{})
		}
		if ok {
			for _, attData := range attachments {
				if am, ok := attData.(map[string]interface{}); ok {
					att := PanelAttachment{
						Type:     getStringField(am, "type"),
						Selector: getStringField(am, "selector"),
						Tag:      getStringField(am, "tag"),
						ID:       getStringField(am, "id"),
						Text:     getStringField(am, "text"),
						Summary:  getStringField(am, "summary"),
						FilePath: getStringField(am, "filePath"),
					}

					// Parse classes
					if classes, ok := am["classes"].([]interface{}); ok {
						for _, c := range classes {
							if s, ok := c.(string); ok {
								att.Classes = append(att.Classes, s)
							}
						}
					}

					// Parse area (for screenshot_area type)
					if area, ok := am["area"].(map[string]interface{}); ok {
						att.Area = &ScreenshotArea{
							X:      getIntField(area, "x"),
							Y:      getIntField(area, "y"),
							Width:  getIntField(area, "width"),
							Height: getIntField(area, "height"),
							Data:   getStringField(area, "data"),
						}
					}

					// Preserve the original data field for additional metadata
					if data, ok := am["data"].(map[string]interface{}); ok {
						att.Data = data
					}

					msg.Attachments = append(msg.Attachments, att)
				}
			}
		}
	}

	return msg
}

// parseSketchEntry parses a sketch entry from JSON data.
func parseSketchEntry(data map[string]interface{}, id string, timestamp time.Time, url string) SketchEntry {
	entry := SketchEntry{
		ID:           id,
		Timestamp:    timestamp,
		URL:          url,
		Description:  getStringField(data, "description"),
		ElementCount: getIntField(data, "element_count"),
		ImageData:    getStringField(data, "image"),
	}

	// Parse sketch data (store as-is for JSON flexibility)
	if sketchData, ok := data["sketch"].(map[string]interface{}); ok {
		entry.Sketch = sketchData
	}

	return entry
}

// parseElementCapture parses an element capture from panel JSON data.
func parseElementCapture(data map[string]interface{}, timestamp time.Time, url string) ElementCapture {
	capture := ElementCapture{
		ID:        getStringField(data, "id"),
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse nested data field
	if nested, ok := data["data"].(map[string]interface{}); ok {
		capture.Summary = getStringField(nested, "summary")
		capture.Selector = getStringField(nested, "selector")
		capture.Tag = getStringField(nested, "tag")
		capture.ElementID = getStringField(nested, "id")
		capture.Text = getStringField(nested, "text")

		// Parse classes array
		if classes, ok := nested["classes"].([]interface{}); ok {
			for _, c := range classes {
				if s, ok := c.(string); ok {
					capture.Classes = append(capture.Classes, s)
				}
			}
		}

		// Parse rect
		if rect, ok := nested["rect"].(map[string]interface{}); ok {
			capture.Rect.X = getFloatField(rect, "x")
			capture.Rect.Y = getFloatField(rect, "y")
			capture.Rect.Width = getFloatField(rect, "width")
			capture.Rect.Height = getFloatField(rect, "height")
		}
	}

	return capture
}

// parseSketchCapture parses a sketch capture from panel JSON data.
func parseSketchCapture(data map[string]interface{}, timestamp time.Time, url string) SketchCapture {
	capture := SketchCapture{
		ID:        getStringField(data, "id"),
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse nested data field
	if nested, ok := data["data"].(map[string]interface{}); ok {
		capture.ElementCount = getIntField(nested, "elementCount")
		capture.Summary = fmt.Sprintf("Sketch with %d elements", capture.ElementCount)
		capture.ImageData = getStringField(nested, "image")

		// Parse sketch data (store as-is for JSON flexibility)
		if sketchData, ok := nested["sketch"].(map[string]interface{}); ok {
			capture.Sketch = sketchData
		}
	}

	return capture
}

func parseDesignState(data map[string]interface{}, id string, timestamp time.Time, url string) DesignState {
	state := DesignState{
		ID:           id,
		Timestamp:    timestamp,
		URL:          url,
		Selector:     getStringField(data, "selector"),
		XPath:        getStringField(data, "xpath"),
		OriginalHTML: getStringField(data, "originalHTML"),
		ContextHTML:  getStringField(data, "contextHTML"),
	}

	// Parse metadata
	if metaData, ok := data["metadata"].(map[string]interface{}); ok {
		state.Metadata = parseDesignElementMetadata(metaData)
	}

	return state
}

func parseDesignRequest(data map[string]interface{}, id string, timestamp time.Time, url string) DesignRequest {
	request := DesignRequest{
		ID:                id,
		Timestamp:         timestamp,
		URL:               url,
		Selector:          getStringField(data, "selector"),
		XPath:             getStringField(data, "xpath"),
		CurrentHTML:       getStringField(data, "currentHTML"),
		OriginalHTML:      getStringField(data, "originalHTML"),
		ContextHTML:       getStringField(data, "contextHTML"),
		AlternativesCount: getIntField(data, "alternativesCount"),
	}

	// Parse metadata
	if metaData, ok := data["metadata"].(map[string]interface{}); ok {
		request.Metadata = parseDesignElementMetadata(metaData)
	}

	// Parse chat history
	if history, ok := data["chatHistory"].([]interface{}); ok {
		for _, item := range history {
			if msgData, ok := item.(map[string]interface{}); ok {
				request.ChatHistory = append(request.ChatHistory, DesignChatMessage{
					Timestamp: getInt64Field(msgData, "timestamp"),
					Message:   getStringField(msgData, "message"),
					Role:      getStringField(msgData, "role"),
				})
			}
		}
	}

	return request
}

func parseDesignChat(data map[string]interface{}, id string, timestamp time.Time, url string) DesignChat {
	chat := DesignChat{
		ID:           id,
		Timestamp:    timestamp,
		URL:          url,
		Message:      getStringField(data, "message"),
		Selector:     getStringField(data, "selector"),
		XPath:        getStringField(data, "xpath"),
		CurrentHTML:  getStringField(data, "currentHTML"),
		OriginalHTML: getStringField(data, "originalHTML"),
		ContextHTML:  getStringField(data, "contextHTML"),
	}

	// Parse metadata
	if metaData, ok := data["metadata"].(map[string]interface{}); ok {
		chat.Metadata = parseDesignElementMetadata(metaData)
	}

	// Parse chat history
	if history, ok := data["chatHistory"].([]interface{}); ok {
		for _, item := range history {
			if msgData, ok := item.(map[string]interface{}); ok {
				chat.ChatHistory = append(chat.ChatHistory, DesignChatMessage{
					Timestamp: getInt64Field(msgData, "timestamp"),
					Message:   getStringField(msgData, "message"),
					Role:      getStringField(msgData, "role"),
				})
			}
		}
	}

	return chat
}

func parseDesignElementMetadata(data map[string]interface{}) DesignElementMetadata {
	metadata := DesignElementMetadata{
		Tag:  getStringField(data, "tag"),
		ID:   getStringField(data, "id"),
		Text: getStringField(data, "text"),
	}

	// Parse classes array
	if classes, ok := data["classes"].([]interface{}); ok {
		for _, class := range classes {
			if classStr, ok := class.(string); ok {
				metadata.Classes = append(metadata.Classes, classStr)
			}
		}
	}

	// Parse attributes
	if attrs, ok := data["attributes"].(map[string]interface{}); ok {
		metadata.Attributes = make(map[string]string)
		for key, val := range attrs {
			if valStr, ok := val.(string); ok {
				metadata.Attributes[key] = valStr
			}
		}
	}

	// Parse rect
	if rect, ok := data["rect"].(map[string]interface{}); ok {
		metadata.Rect.Width = getIntField(rect, "width")
		metadata.Rect.Height = getIntField(rect, "height")
	}

	return metadata
}
