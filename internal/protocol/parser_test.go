package protocol

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

func TestParseCommand_Simple(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Command
		wantErr bool
	}{
		{
			name:  "PING",
			input: "PING;;",
			want:  &Command{Verb: "PING"},
		},
		{
			name:  "INFO",
			input: "INFO;;",
			want:  &Command{Verb: "INFO"},
		},
		{
			name:  "SHUTDOWN",
			input: "SHUTDOWN;;",
			want:  &Command{Verb: "SHUTDOWN"},
		},
		{
			name:  "DETECT with path",
			input: "DETECT /home/user/project;;",
			want:  &Command{Verb: "DETECT", Args: []string{"/home/user/project"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(strings.NewReader(tt.input))
			got, err := parser.ParseCommand()
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Verb != tt.want.Verb {
					t.Errorf("Verb = %v, want %v", got.Verb, tt.want.Verb)
				}
				if len(got.Args) != len(tt.want.Args) {
					t.Errorf("Args = %v, want %v", got.Args, tt.want.Args)
				}
			}
		})
	}
}

func TestParseCommand_WithSubVerb(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Command
		wantErr bool
	}{
		{
			name:  "PROC STATUS",
			input: "PROC STATUS my-process;;",
			want: &Command{
				Verb:    "PROC",
				SubVerb: "STATUS",
				Args:    []string{"my-process"},
			},
		},
		{
			name:  "PROC LIST",
			input: "PROC LIST;;",
			want: &Command{
				Verb:    "PROC",
				SubVerb: "LIST",
			},
		},
		{
			name:  "PROC STOP with force",
			input: "PROC STOP my-process force;;",
			want: &Command{
				Verb:    "PROC",
				SubVerb: "STOP",
				Args:    []string{"my-process", "force"},
			},
		},
		{
			name:  "PROXY START",
			input: "PROXY START dev http://localhost:3000 8080;;",
			want: &Command{
				Verb:    "PROXY",
				SubVerb: "START",
				Args:    []string{"dev", "http://localhost:3000", "8080"},
			},
		},
		{
			name:  "PROXY LIST",
			input: "PROXY LIST;;",
			want: &Command{
				Verb:    "PROXY",
				SubVerb: "LIST",
			},
		},
		{
			name:  "PROXYLOG STATS",
			input: "PROXYLOG STATS dev;;",
			want: &Command{
				Verb:    "PROXYLOG",
				SubVerb: "STATS",
				Args:    []string{"dev"},
			},
		},
		{
			name:  "CURRENTPAGE LIST",
			input: "CURRENTPAGE LIST dev;;",
			want: &Command{
				Verb:    "CURRENTPAGE",
				SubVerb: "LIST",
				Args:    []string{"dev"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(strings.NewReader(tt.input))
			got, err := parser.ParseCommand()
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Verb != tt.want.Verb {
					t.Errorf("Verb = %v, want %v", got.Verb, tt.want.Verb)
				}
				if got.SubVerb != tt.want.SubVerb {
					t.Errorf("SubVerb = %v, want %v", got.SubVerb, tt.want.SubVerb)
				}
				if len(got.Args) != len(tt.want.Args) {
					t.Errorf("Args = %v, want %v", got.Args, tt.want.Args)
				}
			}
		})
	}
}

// Helper to format test input with base64 encoded data
func formatTestCommand(verb string, args []string, data string) string {
	var buf strings.Builder
	buf.WriteString(verb)
	for _, arg := range args {
		buf.WriteByte(' ')
		buf.WriteString(arg)
	}
	if data != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(data))
		buf.WriteString(" -- ")
		buf.WriteString(strconv.Itoa(len(encoded)))
		buf.WriteByte('\n')
		buf.WriteString(encoded)
	}
	buf.WriteString(";;")
	return buf.String()
}

func TestParseCommand_WithData(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantVerb string
		wantArgs []string
		wantData string
		wantErr  bool
	}{
		{
			name:     "RUN-JSON",
			input:    formatTestCommand("RUN-JSON", nil, `{"id":"test","path":".","mode":"background"}`),
			wantVerb: "RUN-JSON",
			wantData: `{"id":"test","path":".","mode":"background"}`,
		},
		{
			name:     "PROXY START with path data",
			input:    formatTestCommand("PROXY START", []string{"dev", "http://localhost:3000", "8080"}, `{"path":"."}`),
			wantVerb: "PROXY",
			wantArgs: []string{"dev", "http://localhost:3000", "8080"},
			wantData: `{"path":"."}`,
		},
		{
			name:     "PROXY EXEC with code",
			input:    formatTestCommand("PROXY EXEC", []string{"dev"}, "document.title"),
			wantVerb: "PROXY",
			wantArgs: []string{"dev"},
			wantData: "document.title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(strings.NewReader(tt.input))
			got, err := parser.ParseCommand()
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Verb != tt.wantVerb {
					t.Errorf("Verb = %v, want %v", got.Verb, tt.wantVerb)
				}
				if string(got.Data) != tt.wantData {
					t.Errorf("Data = %v, want %v", string(got.Data), tt.wantData)
				}
				if len(tt.wantArgs) > 0 {
					if len(got.Args) != len(tt.wantArgs) {
						t.Errorf("Args = %v, want %v", got.Args, tt.wantArgs)
					} else {
						for i, arg := range tt.wantArgs {
							if got.Args[i] != arg {
								t.Errorf("Args[%d] = %v, want %v", i, got.Args[i], arg)
							}
						}
					}
				}
			}
		})
	}
}

// Helper to format test response with base64 encoded data
func formatTestResponse(respType string, data string) string {
	if data == "" {
		return respType + ";;"
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(data))
	return respType + " -- " + strconv.Itoa(len(encoded)) + "\n" + encoded + ";;"
}

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType ResponseType
		wantMsg  string
		wantCode string
		wantData string
		wantErr  bool
	}{
		{
			name:     "OK simple",
			input:    "OK;;",
			wantType: ResponseOK,
		},
		{
			name:     "OK with message",
			input:    "OK 12345;;",
			wantType: ResponseOK,
			wantMsg:  "12345",
		},
		{
			name:     "OK with multi-word message",
			input:    "OK Process started successfully;;",
			wantType: ResponseOK,
			wantMsg:  "Process started successfully",
		},
		{
			name:     "PONG",
			input:    "PONG;;",
			wantType: ResponsePong,
		},
		{
			name:     "ERR",
			input:    "ERR not_found process-123;;",
			wantType: ResponseErr,
			wantCode: "not_found",
			wantMsg:  "process-123",
		},
		{
			name:     "END",
			input:    "END;;",
			wantType: ResponseEnd,
		},
		{
			name:     "JSON",
			input:    formatTestResponse("JSON", `{"status":"running"}`),
			wantType: ResponseJSON,
			wantData: `{"status":"running"}`,
		},
		{
			name:     "DATA",
			input:    formatTestResponse("DATA", "hello"),
			wantType: ResponseData,
			wantData: "hello",
		},
		{
			name:     "CHUNK",
			input:    formatTestResponse("CHUNK", "some bytes"),
			wantType: ResponseChunk,
			wantData: "some bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(strings.NewReader(tt.input))
			got, err := parser.ParseResponse()
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Type != tt.wantType {
					t.Errorf("Type = %v, want %v", got.Type, tt.wantType)
				}
				if got.Message != tt.wantMsg {
					t.Errorf("Message = %v, want %v", got.Message, tt.wantMsg)
				}
				if got.Code != tt.wantCode {
					t.Errorf("Code = %v, want %v", got.Code, tt.wantCode)
				}
				if string(got.Data) != tt.wantData {
					t.Errorf("Data = %v, want %v", string(got.Data), tt.wantData)
				}
			}
		})
	}
}

func TestFormatOK(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"empty", "", "OK;;"},
		{"with message", "12345", "OK 12345;;"},
		{"with text", "started", "OK started;;"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatOK(tt.message)
			if string(got) != tt.want {
				t.Errorf("FormatOK() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatErr(t *testing.T) {
	got := FormatErr(ErrNotFound, "process-123")
	want := "ERR not_found process-123;;"
	if string(got) != want {
		t.Errorf("FormatErr() = %q, want %q", got, want)
	}
}

func TestFormatJSON(t *testing.T) {
	data := []byte(`{"status":"running"}`)
	got := FormatJSON(data)

	// Parse it back
	parser := NewParser(bytes.NewReader(got))
	resp, err := parser.ParseResponse()
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}
	if resp.Type != ResponseJSON {
		t.Errorf("Type = %v, want JSON", resp.Type)
	}
	if string(resp.Data) != string(data) {
		t.Errorf("Data = %v, want %v", string(resp.Data), string(data))
	}
}

func TestFormatChunk(t *testing.T) {
	data := []byte("chunk data")
	got := FormatChunk(data)

	parser := NewParser(bytes.NewReader(got))
	resp, err := parser.ParseResponse()
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}
	if resp.Type != ResponseChunk {
		t.Errorf("Type = %v, want CHUNK", resp.Type)
	}
	if string(resp.Data) != string(data) {
		t.Errorf("Data = %v, want %v", string(resp.Data), string(data))
	}
}

func TestFormatCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  *Command
		want string
	}{
		{
			name: "simple",
			cmd:  &Command{Verb: "PING"},
			want: "PING;;",
		},
		{
			name: "with subverb",
			cmd:  &Command{Verb: "PROC", SubVerb: "STATUS", Args: []string{"test"}},
			want: "PROC STATUS test;;",
		},
		{
			name: "with multiple args",
			cmd:  &Command{Verb: "PROXY", SubVerb: "START", Args: []string{"dev", "http://localhost:3000", "8080"}},
			want: "PROXY START dev http://localhost:3000 8080;;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommand(tt.cmd)
			if string(got) != tt.want {
				t.Errorf("FormatCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCommand_WithData(t *testing.T) {
	// Test command with data - the data gets base64 encoded
	cmd := &Command{Verb: "RUN-JSON", Data: []byte(`{"id":"test"}`)}
	formatted := FormatCommand(cmd)

	// Parse it back to verify round-trip
	parser := NewParser(bytes.NewReader(formatted))
	parsed, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if parsed.Verb != cmd.Verb {
		t.Errorf("Verb = %v, want %v", parsed.Verb, cmd.Verb)
	}
	if string(parsed.Data) != string(cmd.Data) {
		t.Errorf("Data = %v, want %v", string(parsed.Data), string(cmd.Data))
	}
}

func TestWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	// Test WriteOK
	if err := w.WriteOK("12345"); err != nil {
		t.Errorf("WriteOK failed: %v", err)
	}
	if got := buf.String(); got != "OK 12345;;" {
		t.Errorf("WriteOK = %q, want %q", got, "OK 12345;;")
	}
	buf.Reset()

	// Test WriteErr
	if err := w.WriteErr(ErrNotFound, "proc-1"); err != nil {
		t.Errorf("WriteErr failed: %v", err)
	}
	if got := buf.String(); got != "ERR not_found proc-1;;" {
		t.Errorf("WriteErr = %q, want %q", got, "ERR not_found proc-1;;")
	}
	buf.Reset()

	// Test WritePong
	if err := w.WritePong(); err != nil {
		t.Errorf("WritePong failed: %v", err)
	}
	if got := buf.String(); got != "PONG;;" {
		t.Errorf("WritePong = %q, want %q", got, "PONG;;")
	}
	buf.Reset()

	// Test WriteEnd
	if err := w.WriteEnd(); err != nil {
		t.Errorf("WriteEnd failed: %v", err)
	}
	if got := buf.String(); got != "END;;" {
		t.Errorf("WriteEnd = %q, want %q", got, "END;;")
	}
}

func TestRoundTrip(t *testing.T) {
	// Test that formatting and parsing are inverse operations

	// Command round-trip
	originalCmd := &Command{
		Verb:    "PROC",
		SubVerb: "STATUS",
		Args:    []string{"my-process"},
	}

	formatted := FormatCommand(originalCmd)
	parser := NewParser(bytes.NewReader(formatted))
	parsedCmd, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("Failed to parse command: %v", err)
	}

	if parsedCmd.Verb != originalCmd.Verb {
		t.Errorf("Verb = %v, want %v", parsedCmd.Verb, originalCmd.Verb)
	}
	if parsedCmd.SubVerb != originalCmd.SubVerb {
		t.Errorf("SubVerb = %v, want %v", parsedCmd.SubVerb, originalCmd.SubVerb)
	}

	// JSON response round-trip
	jsonData := []byte(`{"id":"test","state":"running","pid":12345}`)
	jsonResp := FormatJSON(jsonData)
	parser = NewParser(bytes.NewReader(jsonResp))
	parsedResp, err := parser.ParseResponse()
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if parsedResp.Type != ResponseJSON {
		t.Errorf("Type = %v, want JSON", parsedResp.Type)
	}
	if string(parsedResp.Data) != string(jsonData) {
		t.Errorf("Data = %v, want %v", string(parsedResp.Data), string(jsonData))
	}
}

func TestParseChunkedResponse(t *testing.T) {
	// Simulate a chunked streaming response with new format
	chunk1 := FormatChunk([]byte("hello"))
	chunk2 := FormatChunk([]byte(" world"))
	end := FormatEnd()

	input := string(chunk1) + string(chunk2) + string(end)
	parser := NewParser(strings.NewReader(input))

	var collected bytes.Buffer

	for {
		resp, err := parser.ParseResponse()
		if err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Type == ResponseEnd {
			break
		}

		if resp.Type == ResponseChunk {
			collected.Write(resp.Data)
		} else {
			t.Fatalf("Unexpected response type: %v", resp.Type)
		}
	}

	if collected.String() != "hello world" {
		t.Errorf("Collected = %q, want %q", collected.String(), "hello world")
	}
}

func TestParseCommand_JSONDetection(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{
			name:    "JSON object instead of command",
			input:   `{"path":"/home/user/project"};;`,
			wantErr: ErrJSONInsteadOfCommand,
		},
		{
			name:    "JSON array instead of command",
			input:   `["ping", "localhost"];;`,
			wantErr: ErrJSONInsteadOfCommand,
		},
		{
			name:    "JSON with whitespace",
			input:   `  {"path":"/home/user"};;`,
			wantErr: ErrJSONInsteadOfCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(strings.NewReader(tt.input))
			_, err := parser.ParseCommand()
			if err != tt.wantErr {
				t.Errorf("ParseCommand() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseCommand_UnknownVerb(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantVerb string
	}{
		{
			name:     "unknown verb",
			input:    "INVALID some args;;",
			wantVerb: "INVALID",
		},
		{
			name:     "another unknown verb",
			input:    "FOOBAR;;",
			wantVerb: "FOOBAR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewParser(strings.NewReader(tt.input))
			_, err := parser.ParseCommand()
			if err == nil {
				t.Error("ParseCommand() expected error, got nil")
				return
			}
			// Check that it's an ErrUnknownCommand with the correct verb
			unknownErr, ok := err.(*ErrUnknownCommand)
			if !ok {
				t.Errorf("ParseCommand() error type = %T, want *ErrUnknownCommand", err)
				return
			}
			if unknownErr.Verb != tt.wantVerb {
				t.Errorf("ErrUnknownCommand.Verb = %q, want %q", unknownErr.Verb, tt.wantVerb)
			}
			if len(unknownErr.ValidVerbs) == 0 {
				t.Error("ErrUnknownCommand.ValidVerbs is empty, want non-empty")
			}
		})
	}
}

func TestVerbRegistry(t *testing.T) {
	// Test that verbs are properly registered via the DefaultRegistry
	validVerbs := []string{"RUN", "RUN-JSON", "PROC", "PROXY", "PROXYLOG", "CURRENTPAGE", "DETECT", "PING", "INFO", "SHUTDOWN"}
	for _, v := range validVerbs {
		if !DefaultRegistry.IsValidVerb(v) {
			t.Errorf("DefaultRegistry.IsValidVerb(%q) = false, want true", v)
		}
	}

	// Registry is case-insensitive
	if !DefaultRegistry.IsValidVerb("ping") {
		t.Errorf("DefaultRegistry.IsValidVerb(\"ping\") = false, want true (case-insensitive)")
	}

	invalidVerbs := []string{"INVALID", "FOO", "BAR"}
	for _, v := range invalidVerbs {
		if DefaultRegistry.IsValidVerb(v) {
			t.Errorf("DefaultRegistry.IsValidVerb(%q) = true, want false", v)
		}
	}
}

func TestResync(t *testing.T) {
	// Test resync after parsing error - should skip to next terminator
	input := "garbage data;;" + "PING;;"
	parser := NewParser(strings.NewReader(input))

	// Skip the garbage
	err := parser.Resync()
	if err != nil {
		t.Fatalf("Resync() failed: %v", err)
	}

	// Now we should be able to parse PING
	cmd, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("ParseCommand() after Resync() failed: %v", err)
	}
	if cmd.Verb != "PING" {
		t.Errorf("Verb = %v, want PING", cmd.Verb)
	}
}

func TestMultipleCommands(t *testing.T) {
	// Test parsing multiple commands in sequence
	input := "PING;;PROC STATUS test;;PROXY LIST;;"
	parser := NewParser(strings.NewReader(input))

	// Parse first command
	cmd1, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("First ParseCommand() failed: %v", err)
	}
	if cmd1.Verb != "PING" {
		t.Errorf("First command Verb = %v, want PING", cmd1.Verb)
	}

	// Parse second command
	cmd2, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("Second ParseCommand() failed: %v", err)
	}
	if cmd2.Verb != "PROC" || cmd2.SubVerb != "STATUS" {
		t.Errorf("Second command = %v %v, want PROC STATUS", cmd2.Verb, cmd2.SubVerb)
	}

	// Parse third command
	cmd3, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("Third ParseCommand() failed: %v", err)
	}
	if cmd3.Verb != "PROXY" || cmd3.SubVerb != "LIST" {
		t.Errorf("Third command = %v %v, want PROXY LIST", cmd3.Verb, cmd3.SubVerb)
	}
}

func TestDataWithSpecialCharacters(t *testing.T) {
	// Test that base64 encoding handles special characters properly
	tests := []struct {
		name string
		data string
	}{
		{
			name: "JavaScript with semicolons",
			data: `console.log("hello"); alert("world");`,
		},
		{
			name: "JSON with nested quotes",
			data: `{"message": "He said \"hello\"", "count": 42}`,
		},
		{
			name: "JavaScript with newlines",
			data: "function test() {\n  return 42;\n}",
		},
		{
			name: "Binary-like content",
			data: string([]byte{0x00, 0x01, 0x02, 0xFF, 0xFE}),
		},
		{
			name: "Command terminator in data",
			data: "this contains ;; the terminator",
		},
		{
			name: "Data marker in data",
			data: "this contains -- the marker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Format a command with this data
			cmd := &Command{
				Verb:    "PROXY",
				SubVerb: "EXEC",
				Args:    []string{"dev"},
				Data:    []byte(tt.data),
			}
			formatted := FormatCommand(cmd)

			// Parse it back
			parser := NewParser(bytes.NewReader(formatted))
			parsed, err := parser.ParseCommand()
			if err != nil {
				t.Fatalf("ParseCommand() failed: %v", err)
			}

			if string(parsed.Data) != tt.data {
				t.Errorf("Data mismatch:\ngot:  %q\nwant: %q", string(parsed.Data), tt.data)
			}
		})
	}
}

func TestResponseWithSpecialCharacters(t *testing.T) {
	// Test that response data also handles special characters properly
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "JSON response",
			data: []byte(`{"message": "Hello;; World", "items": ["a", "b"]}`),
		},
		{
			name: "Response with newlines",
			data: []byte("line1\nline2\nline3"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Format JSON response
			formatted := FormatJSON(tt.data)

			// Parse it back
			parser := NewParser(bytes.NewReader(formatted))
			resp, err := parser.ParseResponse()
			if err != nil {
				t.Fatalf("ParseResponse() failed: %v", err)
			}

			if resp.Type != ResponseJSON {
				t.Errorf("Type = %v, want JSON", resp.Type)
			}
			if string(resp.Data) != string(tt.data) {
				t.Errorf("Data mismatch:\ngot:  %q\nwant: %q", string(resp.Data), string(tt.data))
			}
		})
	}
}
