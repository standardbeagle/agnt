package claude

import (
	"encoding/json"
	"strings"
	"time"
)

// ContentBlock represents the content of a message.
// It can be one of: TextBlock, ThinkingBlock, ToolUseBlock, or ToolResultBlock.
type ContentBlock interface {
	contentBlock()
}

// TextBlock contains plain text content.
type TextBlock struct {
	Type string `json:"type"` // Always "text"
	Text string `json:"text"`
}

func (TextBlock) contentBlock() {}

// ThinkingBlock contains Claude's internal reasoning.
type ThinkingBlock struct {
	Type      string `json:"type"` // Always "thinking"
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

func (ThinkingBlock) contentBlock() {}

// ToolUseBlock represents a tool invocation by Claude.
type ToolUseBlock struct {
	Type  string                 `json:"type"` // Always "tool_use"
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

func (ToolUseBlock) contentBlock() {}

// ToolResultBlock contains the result of a tool execution.
// Content can be a string or an array of content blocks in the JSON;
// UnmarshalJSON normalizes both forms into a single string.
type ToolResultBlock struct {
	Type      string `json:"type"` // Always "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

func (ToolResultBlock) contentBlock() {}

// UnmarshalJSON handles both string and array formats for the content field.
// Claude Code can emit tool results as either:
//
//	"content": "text string"
//	"content": [{"type": "text", "text": "..."}]
func (t *ToolResultBlock) UnmarshalJSON(data []byte) error {
	// Alias to avoid recursive UnmarshalJSON call
	type Alias struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content,omitempty"`
		IsError   bool            `json:"is_error,omitempty"`
	}
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	t.Type = a.Type
	t.ToolUseID = a.ToolUseID
	t.IsError = a.IsError

	if len(a.Content) == 0 {
		return nil
	}

	// Try string first (common case)
	var s string
	if err := json.Unmarshal(a.Content, &s); err == nil {
		t.Content = s
		return nil
	}

	// Try array of content blocks — extract and join text
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(a.Content, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		t.Content = strings.Join(parts, "\n")
		return nil
	}

	return nil
}

// Message is the interface for all message types.
type MessageType interface {
	messageType()
}

// UserMessage represents a message from the user.
type UserMessage struct {
	Type            string          `json:"type"` // Always "user"
	UUID            string          `json:"uuid,omitempty"`
	Content         []ContentBlock  `json:"-"`
	RawContent      json.RawMessage `json:"content,omitempty"`
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`
}

func (UserMessage) messageType() {}

// AssistantMessage represents a message from Claude.
type AssistantMessage struct {
	Type            string          `json:"type"` // Always "assistant"
	UUID            string          `json:"uuid,omitempty"`
	Content         []ContentBlock  `json:"-"`
	RawContent      json.RawMessage `json:"content,omitempty"`
	Model           string          `json:"model,omitempty"`
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`
	IsError         bool            `json:"is_error,omitempty"`
}

func (AssistantMessage) messageType() {}

// SystemMessage represents a system-level message.
type SystemMessage struct {
	Type    string                 `json:"type"` // Always "system"
	Subtype string                 `json:"subtype"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

func (SystemMessage) messageType() {}

// Usage tracks API usage statistics.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read,omitempty"`
	CacheWrite   int `json:"cache_write,omitempty"`
}

// ResultMessage contains metadata about the completed interaction.
type ResultMessage struct {
	Type             string                 `json:"type"` // Always "result"
	DurationMS       int64                  `json:"duration_ms"`
	DurationAPIMS    int64                  `json:"duration_api_ms,omitempty"`
	IsError          bool                   `json:"is_error"`
	NumTurns         int                    `json:"num_turns"`
	SessionID        string                 `json:"session_id"`
	TotalCostUSD     float64                `json:"total_cost_usd,omitempty"`
	Usage            *Usage                 `json:"usage,omitempty"`
	Result           string                 `json:"result,omitempty"`
	StructuredOutput map[string]interface{} `json:"structured_output,omitempty"`
}

func (ResultMessage) messageType() {}

// StreamEvent represents a streaming event from the CLI.
type StreamEvent struct {
	Type            string          `json:"type"` // Always "stream_event"
	UUID            string          `json:"uuid,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	Event           string          `json:"event,omitempty"`
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`
	RawEvent        json.RawMessage `json:"raw_event,omitempty"`
}

func (StreamEvent) messageType() {}

// PermissionMode defines how permissions are handled.
type PermissionMode string

const (
	PermissionModeDefault          PermissionMode = "default"
	PermissionModeAcceptEdits      PermissionMode = "acceptEdits"
	PermissionModePlan             PermissionMode = "plan"
	PermissionModeBypassPermission PermissionMode = "bypassPermissions"
)

// PermissionBehavior defines the behavior for a permission decision.
type PermissionBehavior string

const (
	PermissionBehaviorAllow PermissionBehavior = "allow"
	PermissionBehaviorDeny  PermissionBehavior = "deny"
	PermissionBehaviorAsk   PermissionBehavior = "ask"
)

// ParseContentBlock parses a raw JSON content block into the appropriate type.
func ParseContentBlock(raw json.RawMessage) (ContentBlock, error) {
	var typeCheck struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &typeCheck); err != nil {
		return nil, err
	}

	switch typeCheck.Type {
	case "text":
		var block TextBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		return block, nil
	case "thinking":
		var block ThinkingBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		return block, nil
	case "tool_use":
		var block ToolUseBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		return block, nil
	case "tool_result":
		var block ToolResultBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		return block, nil
	default:
		// Return as text block for unknown types
		return TextBlock{Type: "text", Text: string(raw)}, nil
	}
}

// ParseContentBlocks parses an array of raw JSON content blocks.
func ParseContentBlocks(raw json.RawMessage) ([]ContentBlock, error) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		// Try parsing as a string (simple text content)
		var text string
		if textErr := json.Unmarshal(raw, &text); textErr == nil {
			return []ContentBlock{TextBlock{Type: "text", Text: text}}, nil
		}
		return nil, err
	}

	result := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		cb, err := ParseContentBlock(block)
		if err != nil {
			return nil, err
		}
		result = append(result, cb)
	}
	return result, nil
}

// ParseMessage parses a raw JSON message into the appropriate message type.
func ParseMessage(raw json.RawMessage) (MessageType, error) {
	var typeCheck struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &typeCheck); err != nil {
		return nil, err
	}

	switch typeCheck.Type {
	case "user":
		var msg UserMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		// Handle nested message.content format (Claude Code stream-json)
		if msg.RawContent == nil {
			var nested struct {
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(raw, &nested); err == nil && nested.Message.Content != nil {
				msg.RawContent = nested.Message.Content
			}
		}
		if msg.RawContent != nil {
			blocks, err := ParseContentBlocks(msg.RawContent)
			if err != nil {
				return nil, err
			}
			msg.Content = blocks
		}
		return msg, nil
	case "assistant":
		var msg AssistantMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		// Handle nested message.content format (Claude Code stream-json)
		if msg.RawContent == nil {
			var nested struct {
				Message struct {
					Content json.RawMessage `json:"content"`
					Model   string          `json:"model,omitempty"`
				} `json:"message"`
			}
			if err := json.Unmarshal(raw, &nested); err == nil && nested.Message.Content != nil {
				msg.RawContent = nested.Message.Content
				if nested.Message.Model != "" {
					msg.Model = nested.Message.Model
				}
			}
		}
		if msg.RawContent != nil {
			blocks, err := ParseContentBlocks(msg.RawContent)
			if err != nil {
				return nil, err
			}
			msg.Content = blocks
		}
		return msg, nil
	case "system":
		var msg SystemMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	case "result":
		var msg ResultMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	case "stream_event":
		var msg StreamEvent
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	default:
		// Return as system message for unknown types
		return SystemMessage{Type: typeCheck.Type, Subtype: "unknown"}, nil
	}
}

// GetText extracts text content from a message.
func GetText(msg MessageType) string {
	switch m := msg.(type) {
	case UserMessage:
		return getTextFromBlocks(m.Content)
	case AssistantMessage:
		return getTextFromBlocks(m.Content)
	case ResultMessage:
		return m.Result
	default:
		return ""
	}
}

func getTextFromBlocks(blocks []ContentBlock) string {
	var text string
	for _, block := range blocks {
		if tb, ok := block.(TextBlock); ok {
			if text != "" {
				text += "\n"
			}
			text += tb.Text
		}
	}
	return text
}

// GetToolCalls extracts tool use blocks from an assistant message.
func GetToolCalls(msg AssistantMessage) []ToolUseBlock {
	var tools []ToolUseBlock
	for _, block := range msg.Content {
		if tb, ok := block.(ToolUseBlock); ok {
			tools = append(tools, tb)
		}
	}
	return tools
}

// Timestamp helper for message timing.
type Timestamp time.Time

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).Format(time.RFC3339Nano))
}

func (t *Timestamp) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	*t = Timestamp(parsed)
	return nil
}
