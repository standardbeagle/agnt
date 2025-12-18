package aichannel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Integration tests that verify uniform output handling across different modes.
// These tests use real processes to ensure the output format parsing works correctly.

// TestIntegration_OutputFormats tests all three output formats with real processes.
func TestIntegration_OutputFormats(t *testing.T) {
	// Create a temporary script that outputs in different formats
	tmpDir := t.TempDir()

	// Create a helper script that outputs JSON
	jsonScript := filepath.Join(tmpDir, "json_output.sh")
	err := os.WriteFile(jsonScript, []byte(`#!/bin/bash
echo '{"type":"result","subtype":"success","result":"Integration test response","session_id":"int-123","total_cost_usd":0.001,"duration_ms":100,"num_turns":1}'
`), 0755)
	if err != nil {
		t.Fatalf("Failed to create json script: %v", err)
	}

	// Create a helper script that outputs stream-json
	streamScript := filepath.Join(tmpDir, "stream_output.sh")
	err = os.WriteFile(streamScript, []byte(`#!/bin/bash
echo '{"type":"init","session_id":"stream-456"}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Streaming content"}]}}'
echo '{"type":"result","result":"Final streaming result","session_id":"stream-456","total_cost_usd":0.002}'
`), 0755)
	if err != nil {
		t.Fatalf("Failed to create stream script: %v", err)
	}

	t.Run("TextFormat", func(t *testing.T) {
		ch := NewWithConfig(Config{
			Agent:              AgentCustom,
			Command:            "echo",
			NonInteractiveFlag: "",
			OutputFormat:       "text",
			Timeout:            5 * time.Second,
		})

		resp, err := ch.SendAndParse(context.Background(), "Text response", "")
		if err != nil {
			t.Fatalf("SendAndParse() error = %v", err)
		}

		if resp.Result != "Text response" {
			t.Errorf("Result = %q, want %q", resp.Result, "Text response")
		}
	})

	t.Run("JSONFormat", func(t *testing.T) {
		ch := NewWithConfig(Config{
			Agent:              AgentCustom,
			Command:            jsonScript,
			NonInteractiveFlag: "",
			OutputFormat:       "json",
			OutputFormatFlag:   "--output-format", // Enable JSON support for custom agent
			Timeout:            5 * time.Second,
		})

		resp, err := ch.SendAndParse(context.Background(), "", "")
		if err != nil {
			t.Fatalf("SendAndParse() error = %v", err)
		}

		if resp.Result != "Integration test response" {
			t.Errorf("Result = %q, want %q", resp.Result, "Integration test response")
		}
		if resp.SessionID != "int-123" {
			t.Errorf("SessionID = %q, want %q", resp.SessionID, "int-123")
		}
		if resp.TotalCostUSD != 0.001 {
			t.Errorf("TotalCostUSD = %v, want %v", resp.TotalCostUSD, 0.001)
		}
		if resp.Subtype != "success" {
			t.Errorf("Subtype = %q, want %q", resp.Subtype, "success")
		}
	})

	t.Run("StreamJSONFormat", func(t *testing.T) {
		ch := NewWithConfig(Config{
			Agent:              AgentCustom,
			Command:            streamScript,
			NonInteractiveFlag: "",
			OutputFormat:       "stream-json",
			OutputFormatFlag:   "--output-format", // Enable JSON support for custom agent
			Timeout:            5 * time.Second,
		})

		resp, err := ch.SendAndParse(context.Background(), "", "")
		if err != nil {
			t.Fatalf("SendAndParse() error = %v", err)
		}

		if resp.Result != "Final streaming result" {
			t.Errorf("Result = %q, want %q", resp.Result, "Final streaming result")
		}
		if resp.SessionID != "stream-456" {
			t.Errorf("SessionID = %q, want %q", resp.SessionID, "stream-456")
		}
		if resp.TotalCostUSD != 0.002 {
			t.Errorf("TotalCostUSD = %v, want %v", resp.TotalCostUSD, 0.002)
		}
	})
}

// TestIntegration_UniformAPIContract tests that all formats provide the same basic API.
func TestIntegration_UniformAPIContract(t *testing.T) {
	tmpDir := t.TempDir()

	// All three formats should produce a Response with at minimum a Result field
	formats := []struct {
		name   string
		format OutputFormat
		script string
	}{
		{
			name:   "text",
			format: OutputFormatText,
			script: "#!/bin/bash\necho 'Hello from text'",
		},
		{
			name:   "json",
			format: OutputFormatJSON,
			script: `#!/bin/bash
echo '{"type":"result","result":"Hello from JSON"}'`,
		},
		{
			name:   "stream-json",
			format: OutputFormatStreamJSON,
			script: `#!/bin/bash
echo '{"type":"result","result":"Hello from stream"}'`,
		},
	}

	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			scriptPath := filepath.Join(tmpDir, f.name+"_script.sh")
			err := os.WriteFile(scriptPath, []byte(f.script), 0755)
			if err != nil {
				t.Fatalf("Failed to create script: %v", err)
			}

			ch := NewWithConfig(Config{
				Agent:              AgentCustom,
				Command:            scriptPath,
				NonInteractiveFlag: "",
				OutputFormat:       string(f.format),
				Timeout:            5 * time.Second,
			})

			resp, err := ch.SendAndParse(context.Background(), "", "")
			if err != nil {
				t.Fatalf("SendAndParse() error = %v", err)
			}

			// Verify the contract: Result must be non-empty
			if resp.Result == "" {
				t.Error("Result should not be empty")
			}

			// Response struct should be non-nil
			if resp == nil {
				t.Error("Response should not be nil")
			}
		})
	}
}

// TestIntegration_ExtractLastResponse verifies the convenience function works uniformly.
func TestIntegration_ExtractLastResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		format   OutputFormat
		expected string
	}{
		{
			name:     "Text",
			input:    "Simple text response",
			format:   OutputFormatText,
			expected: "Simple text response",
		},
		{
			name:     "JSON",
			input:    `{"type":"result","result":"JSON response text"}`,
			format:   OutputFormatJSON,
			expected: "JSON response text",
		},
		{
			name: "StreamJSON",
			input: `{"type":"init","session_id":"x"}
{"type":"result","result":"Stream response text"}`,
			format:   OutputFormatStreamJSON,
			expected: "Stream response text",
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

// TestIntegration_PTYMode tests output parsing when running in PTY mode.
func TestIntegration_PTYMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a script that outputs JSON (simulating a PTY-based tool)
	ptyScript := filepath.Join(tmpDir, "pty_json.sh")
	err := os.WriteFile(ptyScript, []byte(`#!/bin/bash
echo '{"type":"result","result":"PTY JSON response","session_id":"pty-789"}'
`), 0755)
	if err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            ptyScript,
		NonInteractiveFlag: "",
		OutputFormat:       "json",
		OutputFormatFlag:   "--output-format", // Enable JSON support for custom agent
		UsePTY:             true,
		Timeout:            5 * time.Second,
	})

	resp, err := ch.SendAndParse(context.Background(), "", "")
	if err != nil {
		t.Fatalf("SendAndParse() error = %v", err)
	}

	if resp.Result != "PTY JSON response" {
		t.Errorf("Result = %q, want %q", resp.Result, "PTY JSON response")
	}
	if resp.SessionID != "pty-789" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "pty-789")
	}
}

// TestIntegration_RealClaude tests with actual Claude CLI if available.
func TestIntegration_RealClaude(t *testing.T) {
	// Skip if claude is not available
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not found in PATH, skipping real Claude test")
	}

	// Skip in CI environments
	if os.Getenv("CI") != "" {
		t.Skip("Skipping real Claude test in CI")
	}

	t.Run("JSONFormat", func(t *testing.T) {
		t.Skip("Manual test - uncomment to run with real Claude")

		ch := NewWithConfig(Config{
			Agent:        AgentClaude,
			OutputFormat: "json",
			Timeout:      30 * time.Second,
		})

		resp, err := ch.SendAndParse(context.Background(), "Say exactly: test response", "")
		if err != nil {
			t.Fatalf("SendAndParse() error = %v", err)
		}

		t.Logf("Result: %s", resp.Result)
		t.Logf("SessionID: %s", resp.SessionID)
		t.Logf("Cost: $%.4f", resp.TotalCostUSD)
		t.Logf("Duration: %dms", resp.DurationMS)
		t.Logf("Turns: %d", resp.NumTurns)

		if resp.Result == "" {
			t.Error("Expected non-empty result")
		}
	})
}

// TestIntegration_ErrorHandling tests error scenarios produce consistent errors.
func TestIntegration_ErrorHandling(t *testing.T) {
	t.Run("InvalidJSON", func(t *testing.T) {
		_, err := ParseResponse("not valid json", OutputFormatJSON)
		if err == nil {
			t.Error("Expected error for invalid JSON")
		}
	})

	t.Run("EmptyStreamJSON", func(t *testing.T) {
		_, err := ParseResponse("", OutputFormatStreamJSON)
		if err == nil {
			t.Error("Expected error for empty stream-json")
		}
	})

	t.Run("NoResultInStream", func(t *testing.T) {
		_, err := ParseResponse(`{"type":"init"}`, OutputFormatStreamJSON)
		if err == nil {
			t.Error("Expected error when stream has no result")
		}
	})
}

// TestIntegration_MixedOutputJSON tests JSON extraction from mixed PTY output.
func TestIntegration_MixedOutputJSON(t *testing.T) {
	t.Run("JSONWithPrefixText", func(t *testing.T) {
		// Simulate PTY output with progress message before JSON
		mixedOutput := `Loading...
Processing request...
{"type":"result","result":"Hello world","session_id":"abc123"}`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != "Hello world" {
			t.Errorf("Result = %q, want %q", resp.Result, "Hello world")
		}
		if resp.SessionID != "abc123" {
			t.Errorf("SessionID = %q, want %q", resp.SessionID, "abc123")
		}
	})

	t.Run("JSONWithSuffixText", func(t *testing.T) {
		// JSON followed by extra text
		mixedOutput := `{"type":"result","result":"Test output"}
Done.`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != "Test output" {
			t.Errorf("Result = %q, want %q", resp.Result, "Test output")
		}
	})

	t.Run("JSONWithANSIRemnants", func(t *testing.T) {
		// Some ANSI codes that might not be stripped, plus JSON
		mixedOutput := `[2K[1G{"type":"result","result":"ANSI test"}`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != "ANSI test" {
			t.Errorf("Result = %q, want %q", resp.Result, "ANSI test")
		}
	})

	t.Run("NestedJSONInResult", func(t *testing.T) {
		// Result containing JSON-like content
		mixedOutput := `prefix {"type":"result","result":"config: {\"key\": \"value\"}"}`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != `config: {"key": "value"}` {
			t.Errorf("Result = %q, want %q", resp.Result, `config: {"key": "value"}`)
		}
	})

	t.Run("MultipleJSONObjects_PrefersTypeResult", func(t *testing.T) {
		// Multiple JSON objects - should prefer type:"result"
		mixedOutput := `{"type":"init","session_id":"sess1"}
{"type":"assistant","message":"working..."}
{"type":"result","result":"Final answer","is_error":false}`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		// Should specifically get the type:"result" object
		if resp.Result != "Final answer" {
			t.Errorf("Expected 'Final answer' from type:result object, got %q", resp.Result)
		}
	})

	t.Run("MultipleJSONObjects_FallsBackToTypeField", func(t *testing.T) {
		// No type:"result", but has objects with type field
		mixedOutput := `{"unrelated": true}
{"type":"assistant","result":"Fallback answer"}`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		// Should get the object with "type" field
		if resp.Result != "Fallback answer" {
			t.Errorf("Expected 'Fallback answer', got %q", resp.Result)
		}
	})

	t.Run("MultipleJSONObjects_FallsBackToLast", func(t *testing.T) {
		// No Claude Code format, falls back to last valid JSON
		mixedOutput := `{"partial": true}
{"data": "value", "result": "Generic JSON"}`
		resp, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		// Should get the last valid JSON object
		if resp.Result != "Generic JSON" {
			t.Errorf("Expected 'Generic JSON' from last object, got %q", resp.Result)
		}
	})

	t.Run("NoValidJSON", func(t *testing.T) {
		mixedOutput := `This is just text with no JSON at all`
		_, err := ParseResponse(mixedOutput, OutputFormatJSON)
		if err == nil {
			t.Error("Expected error when no JSON found")
		}
	})
}

// TestIntegration_ExtractEmbeddedJSON tests extraction of AI-embedded JSON in prose.
func TestIntegration_ExtractEmbeddedJSON(t *testing.T) {
	t.Run("JSONAtEnd", func(t *testing.T) {
		// AI puts JSON at end of response
		output := `Here's the configuration you requested:
{"name": "test", "value": 42}`
		result := extractEmbeddedJSON(output)
		if result == "" {
			t.Fatal("Expected to find JSON")
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(result), &obj); err != nil {
			t.Fatalf("Result is not valid JSON: %v", err)
		}
		if obj["name"] != "test" {
			t.Errorf("name = %v, want 'test'", obj["name"])
		}
	})

	t.Run("JSONInMiddle", func(t *testing.T) {
		// AI puts JSON in middle with explanation after
		output := `The result is:
{"status": "success", "count": 5}
Let me know if you need anything else.`
		result := extractEmbeddedJSON(output)
		if result == "" {
			t.Fatal("Expected to find JSON")
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(result), &obj); err != nil {
			t.Fatalf("Result is not valid JSON: %v", err)
		}
		if obj["status"] != "success" {
			t.Errorf("status = %v, want 'success'", obj["status"])
		}
	})

	t.Run("MultipleJSONReturnsLast", func(t *testing.T) {
		// Multiple JSON objects - returns last (final answer)
		output := `First attempt: {"attempt": 1}
Refined: {"attempt": 2, "final": true}`
		result := extractEmbeddedJSON(output)
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(result), &obj); err != nil {
			t.Fatalf("Result is not valid JSON: %v", err)
		}
		if obj["attempt"] != float64(2) {
			t.Errorf("Should return last JSON, got attempt=%v", obj["attempt"])
		}
	})
}

// TestIntegration_MultilineResults tests that multiline results are handled correctly.
func TestIntegration_MultilineResults(t *testing.T) {
	multilineText := "Line 1\nLine 2\nLine 3"

	t.Run("Text", func(t *testing.T) {
		resp, err := ParseResponse(multilineText, OutputFormatText)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != multilineText {
			t.Errorf("Result = %q, want %q", resp.Result, multilineText)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		jsonInput := `{"type":"result","result":"Line 1\nLine 2\nLine 3"}`
		resp, err := ParseResponse(jsonInput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != multilineText {
			t.Errorf("Result = %q, want %q", resp.Result, multilineText)
		}
	})

	t.Run("StreamJSON", func(t *testing.T) {
		streamInput := `{"type":"init"}
{"type":"result","result":"Line 1\nLine 2\nLine 3"}`
		resp, err := ParseResponse(streamInput, OutputFormatStreamJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != multilineText {
			t.Errorf("Result = %q, want %q", resp.Result, multilineText)
		}
	})
}

// TestIntegration_SpecialCharacters tests handling of special characters in output.
func TestIntegration_SpecialCharacters(t *testing.T) {
	special := `Test with "quotes", 'apostrophes', and \backslashes\`

	t.Run("Text", func(t *testing.T) {
		resp, err := ParseResponse(special, OutputFormatText)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != special {
			t.Errorf("Result = %q, want %q", resp.Result, special)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		// JSON-escaped version
		jsonInput := `{"type":"result","result":"Test with \"quotes\", 'apostrophes', and \\backslashes\\"}`
		resp, err := ParseResponse(jsonInput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != special {
			t.Errorf("Result = %q, want %q", resp.Result, special)
		}
	})
}

// TestIntegration_UnicodeContent tests handling of unicode characters.
func TestIntegration_UnicodeContent(t *testing.T) {
	unicode := "Hello 世界 🌍 مرحبا"

	t.Run("Text", func(t *testing.T) {
		resp, err := ParseResponse(unicode, OutputFormatText)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != unicode {
			t.Errorf("Result = %q, want %q", resp.Result, unicode)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		jsonInput := `{"type":"result","result":"Hello 世界 🌍 مرحبا"}`
		resp, err := ParseResponse(jsonInput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if resp.Result != unicode {
			t.Errorf("Result = %q, want %q", resp.Result, unicode)
		}
	})
}

// TestIntegration_LargeOutput tests handling of large outputs.
func TestIntegration_LargeOutput(t *testing.T) {
	// Generate a large string (100KB)
	largeContent := strings.Repeat("x", 100*1024)

	t.Run("Text", func(t *testing.T) {
		resp, err := ParseResponse(largeContent, OutputFormatText)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if len(resp.Result) != len(largeContent) {
			t.Errorf("Result length = %d, want %d", len(resp.Result), len(largeContent))
		}
	})

	t.Run("JSON", func(t *testing.T) {
		// Create JSON with large content
		jsonInput := `{"type":"result","result":"` + largeContent + `"}`
		resp, err := ParseResponse(jsonInput, OutputFormatJSON)
		if err != nil {
			t.Fatalf("Error: %v", err)
		}
		if len(resp.Result) != len(largeContent) {
			t.Errorf("Result length = %d, want %d", len(resp.Result), len(largeContent))
		}
	})
}

// BenchmarkParseResponse benchmarks the different parsing modes.
func BenchmarkParseResponse(b *testing.B) {
	textInput := "Simple text response for benchmarking"
	jsonInput := `{"type":"result","result":"JSON response for benchmarking","session_id":"bench-001","total_cost_usd":0.001}`
	streamInput := `{"type":"init","session_id":"bench-002"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Response"}]}}
{"type":"result","result":"Stream response for benchmarking"}`

	b.Run("Text", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = ParseResponse(textInput, OutputFormatText)
		}
	})

	b.Run("JSON", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = ParseResponse(jsonInput, OutputFormatJSON)
		}
	})

	b.Run("StreamJSON", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = ParseResponse(streamInput, OutputFormatStreamJSON)
		}
	})
}
