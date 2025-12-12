package aichannel

import (
	"strings"
	"testing"
)

func TestParseTextResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple text",
			input:    "Hello world",
			expected: "Hello world",
		},
		{
			name:     "Text with leading/trailing whitespace",
			input:    "  Response text here  \n",
			expected: "Response text here",
		},
		{
			name:     "Multi-line response",
			input:    "Line 1\nLine 2\nLine 3",
			expected: "Line 1\nLine 2\nLine 3",
		},
		{
			name:     "Empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "Only whitespace",
			input:    "   \n\t  ",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ParseResponse(tt.input, OutputFormatText)
			if err != nil {
				t.Fatalf("ParseResponse() error = %v", err)
			}
			if resp.Result != tt.expected {
				t.Errorf("Result = %q, want %q", resp.Result, tt.expected)
			}
		})
	}
}

func TestParseJSONResponse(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantResult  string
		wantSession string
		wantCost    float64
		wantTurns   int
		wantError   bool
	}{
		{
			name: "Complete success response",
			input: `{
				"type": "result",
				"subtype": "success",
				"total_cost_usd": 0.003,
				"is_error": false,
				"duration_ms": 1234,
				"duration_api_ms": 800,
				"num_turns": 6,
				"result": "The response text here",
				"session_id": "abc123"
			}`,
			wantResult:  "The response text here",
			wantSession: "abc123",
			wantCost:    0.003,
			wantTurns:   6,
			wantError:   false,
		},
		{
			name: "Error response",
			input: `{
				"type": "result",
				"subtype": "error",
				"is_error": true,
				"result": "An error occurred",
				"session_id": "xyz789"
			}`,
			wantResult:  "An error occurred",
			wantSession: "xyz789",
			wantCost:    0,
			wantTurns:   0,
			wantError:   true,
		},
		{
			name: "Minimal response",
			input: `{
				"type": "result",
				"result": "Just the result"
			}`,
			wantResult:  "Just the result",
			wantSession: "",
			wantCost:    0,
			wantTurns:   0,
			wantError:   false,
		},
		{
			name: "Response with multiline result",
			input: `{
				"type": "result",
				"result": "Line 1\nLine 2\nLine 3"
			}`,
			wantResult:  "Line 1\nLine 2\nLine 3",
			wantSession: "",
			wantCost:    0,
			wantTurns:   0,
			wantError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ParseResponse(tt.input, OutputFormatJSON)
			if err != nil {
				t.Fatalf("ParseResponse() error = %v", err)
			}
			if resp.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q", resp.Result, tt.wantResult)
			}
			if resp.SessionID != tt.wantSession {
				t.Errorf("SessionID = %q, want %q", resp.SessionID, tt.wantSession)
			}
			if resp.TotalCostUSD != tt.wantCost {
				t.Errorf("TotalCostUSD = %v, want %v", resp.TotalCostUSD, tt.wantCost)
			}
			if resp.NumTurns != tt.wantTurns {
				t.Errorf("NumTurns = %d, want %d", resp.NumTurns, tt.wantTurns)
			}
			if resp.IsError != tt.wantError {
				t.Errorf("IsError = %v, want %v", resp.IsError, tt.wantError)
			}
		})
	}
}

func TestParseJSONResponse_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "Empty input",
			input: "",
		},
		{
			name:  "Invalid JSON",
			input: "not valid json",
		},
		{
			name:  "Truncated JSON",
			input: `{"type": "result", "result":`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseResponse(tt.input, OutputFormatJSON)
			if err == nil {
				t.Error("Expected error for invalid JSON input")
			}
		})
	}
}

func TestParseStreamJSONResponse(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantResult  string
		wantSession string
		wantCost    float64
		wantTurns   int
	}{
		{
			name: "Complete stream with result",
			input: `{"type":"init","session_id":"abc123"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.001,"duration_ms":500,"num_turns":1,"result":"Hi there!","session_id":"abc123"}`,
			wantResult:  "Hi there!",
			wantSession: "abc123",
			wantCost:    0.001,
			wantTurns:   1,
		},
		{
			name: "Stream with multiple assistant messages",
			input: `{"type":"init","session_id":"xyz"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Part 1. "}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Part 2."}]}}
{"type":"result","result":"Part 1. Part 2.","session_id":"xyz"}`,
			wantResult:  "Part 1. Part 2.",
			wantSession: "xyz",
			wantCost:    0,
			wantTurns:   0,
		},
		{
			name: "Stream with blank lines",
			input: `{"type":"init","session_id":"test"}

{"type":"result","result":"Final result"}

`,
			wantResult:  "Final result",
			wantSession: "test",
			wantCost:    0,
			wantTurns:   0,
		},
		{
			name: "Stream with malformed lines",
			input: `{"type":"init","session_id":"test"}
this is not json
{"type":"result","result":"Valid result"}
another bad line`,
			wantResult:  "Valid result",
			wantSession: "test",
			wantCost:    0,
			wantTurns:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ParseResponse(tt.input, OutputFormatStreamJSON)
			if err != nil {
				t.Fatalf("ParseResponse() error = %v", err)
			}
			if resp.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q", resp.Result, tt.wantResult)
			}
			if resp.SessionID != tt.wantSession {
				t.Errorf("SessionID = %q, want %q", resp.SessionID, tt.wantSession)
			}
			if resp.TotalCostUSD != tt.wantCost {
				t.Errorf("TotalCostUSD = %v, want %v", resp.TotalCostUSD, tt.wantCost)
			}
			if resp.NumTurns != tt.wantTurns {
				t.Errorf("NumTurns = %d, want %d", resp.NumTurns, tt.wantTurns)
			}
		})
	}
}

func TestParseStreamJSONResponse_FallbackToAssistant(t *testing.T) {
	// When there's no result message, fall back to accumulated assistant content
	input := `{"type":"init","session_id":"test"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Response without result message"}]}}`

	resp, err := ParseResponse(input, OutputFormatStreamJSON)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if resp.Result != "Response without result message" {
		t.Errorf("Result = %q, want %q", resp.Result, "Response without result message")
	}
}

func TestParseStreamJSONResponse_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "Empty input",
			input: "",
		},
		{
			name:  "No result or assistant messages",
			input: `{"type":"init","session_id":"test"}`,
		},
		{
			name:  "Only whitespace",
			input: "   \n\t  ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseResponse(tt.input, OutputFormatStreamJSON)
			if err == nil {
				t.Error("Expected error for input with no result")
			}
		})
	}
}

func TestParseStreamJSONReader(t *testing.T) {
	// Test real-time stream parsing
	input := `{"type":"init","session_id":"realtime"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Streaming response"}]}}
{"type":"result","result":"Streaming response","session_id":"realtime"}`

	reader := strings.NewReader(input)
	resp, err := ParseStreamJSONReader(reader)
	if err != nil {
		t.Fatalf("ParseStreamJSONReader() error = %v", err)
	}
	if resp.Result != "Streaming response" {
		t.Errorf("Result = %q, want %q", resp.Result, "Streaming response")
	}
	if resp.SessionID != "realtime" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "realtime")
	}
}

func TestExtractLastResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		format   OutputFormat
		expected string
	}{
		{
			name:     "Text format",
			input:    "Simple response",
			format:   OutputFormatText,
			expected: "Simple response",
		},
		{
			name:     "JSON format",
			input:    `{"type":"result","result":"JSON response"}`,
			format:   OutputFormatJSON,
			expected: "JSON response",
		},
		{
			name: "Stream-JSON format",
			input: `{"type":"init"}
{"type":"result","result":"Stream response"}`,
			format:   OutputFormatStreamJSON,
			expected: "Stream response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractLastResponse(tt.input, tt.format)
			if err != nil {
				t.Fatalf("ExtractLastResponse() error = %v", err)
			}
			if result != tt.expected {
				t.Errorf("Result = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractLastResponse_Errors(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		format OutputFormat
	}{
		{
			name:   "Empty JSON",
			input:  "",
			format: OutputFormatJSON,
		},
		{
			name:   "Invalid JSON",
			input:  "not json",
			format: OutputFormatJSON,
		},
		{
			name:   "Empty stream-json",
			input:  "",
			format: OutputFormatStreamJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExtractLastResponse(tt.input, tt.format)
			if err == nil {
				t.Error("Expected error")
			}
		})
	}
}

func TestUnknownFormatFallsBackToText(t *testing.T) {
	resp, err := ParseResponse("Some text", OutputFormat("unknown"))
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if resp.Result != "Some text" {
		t.Errorf("Result = %q, want %q", resp.Result, "Some text")
	}
}

// TestResponseStructFields tests that all Response fields are properly populated.
func TestResponseStructFields(t *testing.T) {
	input := `{
		"type": "result",
		"subtype": "success",
		"total_cost_usd": 0.0123,
		"is_error": false,
		"duration_ms": 5000,
		"duration_api_ms": 4500,
		"num_turns": 3,
		"result": "Complete response",
		"session_id": "session-001"
	}`

	resp, err := ParseResponse(input, OutputFormatJSON)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}

	// Verify all fields
	if resp.Result != "Complete response" {
		t.Errorf("Result = %q, want %q", resp.Result, "Complete response")
	}
	if resp.SessionID != "session-001" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "session-001")
	}
	if resp.TotalCostUSD != 0.0123 {
		t.Errorf("TotalCostUSD = %v, want %v", resp.TotalCostUSD, 0.0123)
	}
	if resp.DurationMS != 5000 {
		t.Errorf("DurationMS = %d, want %d", resp.DurationMS, 5000)
	}
	if resp.DurationAPIMS != 4500 {
		t.Errorf("DurationAPIMS = %d, want %d", resp.DurationAPIMS, 4500)
	}
	if resp.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want %d", resp.NumTurns, 3)
	}
	if resp.IsError != false {
		t.Errorf("IsError = %v, want %v", resp.IsError, false)
	}
	if resp.Subtype != "success" {
		t.Errorf("Subtype = %q, want %q", resp.Subtype, "success")
	}
}
