// Package aichannel provides an interface for communicating with AI coding agents via CLI.
//
// # JSON Extraction Scenarios
//
// There are two distinct scenarios for extracting JSON from AI responses:
//
// ## 1. Claude Code CLI Output Format (extractClaudeCodeJSON)
//
// When using Claude Code CLI with --output-format json, the response is wrapped in a
// structured JSON envelope:
//
//	{"type":"result","result":"<AI's text response>","session_id":"..."}
//
// The AI's actual response is in the "result" field. Use OutputFormatJSON with
// parseJSONResponse, which internally uses extractClaudeCodeJSON to find the
// type:"result" object.
//
// Prompt pattern: No special JSON instructions needed - the AI returns plain text,
// Claude Code wraps it in JSON.
//
// ## 2. AI-Embedded JSON in Prose (extractEmbeddedJSON)
//
// When you want the AI to return structured data AS JSON in its text response.
// Use OutputFormatText and call extractEmbeddedJSON on the result.
//
// Prompt pattern: Explicitly instruct the AI to output JSON:
//
//	"Return your response as a JSON object with the following structure:
//	 {\"status\": \"ok|error\", \"items\": [...], \"summary\": \"...\"}"
//
// Or use BuildEmbeddedJSONPrompt helper.
package aichannel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OutputFormat represents the format of CLI output.
type OutputFormat string

const (
	OutputFormatText       OutputFormat = "text"
	OutputFormatJSON       OutputFormat = "json"
	OutputFormatStreamJSON OutputFormat = "stream-json"
)

// Response represents the parsed response from an AI agent.
type Response struct {
	// Result is the final text response from the agent
	Result string `json:"result"`

	// SessionID uniquely identifies the conversation session
	SessionID string `json:"session_id,omitempty"`

	// TotalCostUSD is the API cost for Claude-based agents
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`

	// DurationMS is the total elapsed time in milliseconds
	DurationMS int64 `json:"duration_ms,omitempty"`

	// DurationAPIMS is the time spent calling the API
	DurationAPIMS int64 `json:"duration_api_ms,omitempty"`

	// NumTurns is the number of conversation turns
	NumTurns int `json:"num_turns,omitempty"`

	// IsError indicates if the response represents an error
	IsError bool `json:"is_error,omitempty"`

	// Subtype is the result type (success/error) for JSON formats
	Subtype string `json:"subtype,omitempty"`
}

// JSONResponse is the structure returned by --output-format json.
type JSONResponse struct {
	Type          string  `json:"type"`
	Subtype       string  `json:"subtype"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	IsError       bool    `json:"is_error"`
	DurationMS    int64   `json:"duration_ms"`
	DurationAPIMS int64   `json:"duration_api_ms"`
	NumTurns      int     `json:"num_turns"`
	Result        string  `json:"result"`
	SessionID     string  `json:"session_id"`
}

// StreamJSONMessage is a single message in stream-json format.
type StreamJSONMessage struct {
	Type      string         `json:"type"`
	Subtype   string         `json:"subtype,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Message   *StreamMessage `json:"message,omitempty"`
	// Result fields for type=="result"
	TotalCostUSD  float64 `json:"total_cost_usd,omitempty"`
	DurationMS    int64   `json:"duration_ms,omitempty"`
	DurationAPIMS int64   `json:"duration_api_ms,omitempty"`
	NumTurns      int     `json:"num_turns,omitempty"`
	Result        string  `json:"result,omitempty"`
	IsError       bool    `json:"is_error,omitempty"`
}

// StreamMessage represents a message in stream-json output.
type StreamMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock represents a content block in a message.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ParseResponse parses the raw output from an AI agent based on the output format.
func ParseResponse(output string, format OutputFormat) (*Response, error) {
	switch format {
	case OutputFormatText:
		return parseTextResponse(output)
	case OutputFormatJSON:
		return parseJSONResponse(output)
	case OutputFormatStreamJSON:
		return parseStreamJSONResponse(output)
	default:
		// Assume text format for unknown formats
		return parseTextResponse(output)
	}
}

// parseTextResponse parses plain text output.
func parseTextResponse(output string) (*Response, error) {
	return &Response{
		Result: strings.TrimSpace(output),
	}, nil
}

// parseJSONResponse parses JSON format output.
// It attempts to extract JSON from mixed output that may contain
// non-JSON prefix/suffix (e.g., progress messages from PTY output).
func parseJSONResponse(output string) (*Response, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, fmt.Errorf("empty JSON response")
	}

	// Try parsing as-is first
	var jsonResp JSONResponse
	if err := json.Unmarshal([]byte(output), &jsonResp); err == nil {
		return &Response{
			Result:        jsonResp.Result,
			SessionID:     jsonResp.SessionID,
			TotalCostUSD:  jsonResp.TotalCostUSD,
			DurationMS:    jsonResp.DurationMS,
			DurationAPIMS: jsonResp.DurationAPIMS,
			NumTurns:      jsonResp.NumTurns,
			IsError:       jsonResp.IsError,
			Subtype:       jsonResp.Subtype,
		}, nil
	}

	// Try to extract Claude Code's JSON format from mixed PTY output
	jsonStr := extractClaudeCodeJSON(output)
	if jsonStr == "" {
		return nil, fmt.Errorf("failed to parse JSON response: no valid Claude Code JSON object found in output")
	}

	if err := json.Unmarshal([]byte(jsonStr), &jsonResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &Response{
		Result:        jsonResp.Result,
		SessionID:     jsonResp.SessionID,
		TotalCostUSD:  jsonResp.TotalCostUSD,
		DurationMS:    jsonResp.DurationMS,
		DurationAPIMS: jsonResp.DurationAPIMS,
		NumTurns:      jsonResp.NumTurns,
		IsError:       jsonResp.IsError,
		Subtype:       jsonResp.Subtype,
	}, nil
}

// extractClaudeCodeJSON extracts Claude Code's JSON response format from mixed output.
// Claude Code's --output-format json produces objects with "type" field (e.g., "result").
// This function specifically looks for that format, preferring objects with type:"result".
func extractClaudeCodeJSON(output string) string {
	candidates := extractAllJSONObjects(output)

	// First pass: look for type:"result" (the final response)
	for _, candidate := range candidates {
		var obj map[string]interface{}
		if json.Unmarshal([]byte(candidate), &obj) == nil {
			if typeVal, ok := obj["type"].(string); ok && typeVal == "result" {
				return candidate
			}
		}
	}

	// Second pass: any object with a "type" field (Claude Code format)
	for _, candidate := range candidates {
		var obj map[string]interface{}
		if json.Unmarshal([]byte(candidate), &obj) == nil {
			if _, ok := obj["type"]; ok {
				return candidate
			}
		}
	}

	// Fallback: return the last valid JSON object
	if len(candidates) > 0 {
		return candidates[len(candidates)-1]
	}
	return ""
}

// extractEmbeddedJSON extracts JSON that an AI has embedded in its text response.
// This is for cases where we prompt the AI to return structured data in its prose.
// It looks for JSON objects, preferring those that look like intentional data structures.
func extractEmbeddedJSON(output string) string {
	candidates := extractAllJSONObjects(output)

	// Return the last JSON object (AI typically puts structured output at the end)
	if len(candidates) > 0 {
		return candidates[len(candidates)-1]
	}
	return ""
}

// extractAllJSONObjects finds all complete JSON objects in the output.
// Returns them in order of appearance.
func extractAllJSONObjects(output string) []string {
	var results []string

	for i := 0; i < len(output); i++ {
		if output[i] != '{' {
			continue
		}

		// Try to find matching closing brace
		depth := 0
		inString := false
		escaped := false
		for j := i; j < len(output); j++ {
			ch := output[j]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && inString {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					// Found complete JSON object, validate it
					candidate := output[i : j+1]
					var test map[string]interface{}
					if json.Unmarshal([]byte(candidate), &test) == nil {
						results = append(results, candidate)
					}
					i = j // Skip past this object
					break
				}
			}
		}
	}
	return results
}

// BuildEmbeddedJSONPrompt creates a prompt that instructs the AI to return
// structured JSON in its text response. Use with OutputFormatText and
// extractEmbeddedJSON.
//
// Example:
//
//	prompt := BuildEmbeddedJSONPrompt(
//	    "Analyze the system status",
//	    `{"status": "ok|error", "issues": ["..."], "summary": "..."}`,
//	)
func BuildEmbeddedJSONPrompt(instruction string, jsonSchema string) string {
	return fmt.Sprintf(`%s

IMPORTANT: Return your response as a valid JSON object with this exact structure:
%s

Output ONLY the JSON object, no additional text or markdown formatting.`, instruction, jsonSchema)
}

// BuildEmbeddedJSONSystemPrompt creates a system prompt for API mode that
// instructs the AI to always return JSON responses.
func BuildEmbeddedJSONSystemPrompt(basePrompt string, jsonSchema string) string {
	return fmt.Sprintf(`%s

You must always respond with a valid JSON object matching this schema:
%s

Output only the JSON object with no markdown code fences or additional text.`, basePrompt, jsonSchema)
}

// parseStreamJSONResponse parses stream-json (JSONL) format output.
// It extracts the final result from the stream of messages.
func parseStreamJSONResponse(output string) (*Response, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, fmt.Errorf("empty stream-json response")
	}

	reader := strings.NewReader(output)
	return ParseStreamJSONReader(reader)
}

// ParseStreamJSONReader parses stream-json from a reader (for real-time processing).
func ParseStreamJSONReader(reader io.Reader) (*Response, error) {
	scanner := bufio.NewScanner(reader)

	var response *Response
	var lastAssistantContent strings.Builder
	var sessionID string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var msg StreamJSONMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Skip malformed lines in stream
			continue
		}

		switch msg.Type {
		case "init", "system":
			if msg.SessionID != "" {
				sessionID = msg.SessionID
			}

		case "assistant":
			// Accumulate assistant responses
			if msg.Message != nil {
				for _, block := range msg.Message.Content {
					if block.Type == "text" {
						lastAssistantContent.WriteString(block.Text)
					}
				}
			}

		case "result":
			// Final result message contains the complete response
			response = &Response{
				Result:        msg.Result,
				SessionID:     msg.SessionID,
				TotalCostUSD:  msg.TotalCostUSD,
				DurationMS:    msg.DurationMS,
				DurationAPIMS: msg.DurationAPIMS,
				NumTurns:      msg.NumTurns,
				IsError:       msg.IsError,
				Subtype:       msg.Subtype,
			}
			if response.SessionID == "" {
				response.SessionID = sessionID
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// If we got a result message, use it
	if response != nil {
		return response, nil
	}

	// Fall back to accumulated assistant content if no result message
	if lastAssistantContent.Len() > 0 {
		return &Response{
			Result:    strings.TrimSpace(lastAssistantContent.String()),
			SessionID: sessionID,
		}, nil
	}

	return nil, fmt.Errorf("no result found in stream-json response")
}

// ExtractLastResponse extracts just the final response text from any format.
// This is a convenience method for when you only care about the result.
func ExtractLastResponse(output string, format OutputFormat) (string, error) {
	resp, err := ParseResponse(output, format)
	if err != nil {
		return "", err
	}
	return resp.Result, nil
}
