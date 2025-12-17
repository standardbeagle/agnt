package protocol

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Protocol constants for resilient parsing
const (
	// CommandTerminator marks the end of a command (including any data payload)
	CommandTerminator = ";;"

	// DataMarker separates arguments from data length
	DataMarker = "--"
)

// Parser handles parsing of protocol commands and responses.
// Commands use explicit terminators for resilience:
//   - Commands end with ";;"
//   - Data is indicated by "--" followed by length
//
// Format:
//
//	VERB [SUBVERB] [ARGS...] [-- LENGTH\nDATA];;
//
// Examples:
//
//	PING;;
//	PROC STATUS my-process;;
//	PROXY START dev http://localhost:3000 8080;;
//	PROXY START dev http://localhost:3000 8080 -- 12\n{"path":"."};;
type Parser struct {
	reader *bufio.Reader
}

// NewParser creates a new protocol parser.
func NewParser(r io.Reader) *Parser {
	return &Parser{
		reader: bufio.NewReader(r),
	}
}

// ValidVerbs lists all valid command verbs.
var ValidVerbs = []string{
	VerbRun, VerbRunJSON, VerbProc, VerbProxy, VerbProxyLog,
	VerbCurrentPage, VerbTunnel, VerbChaos, VerbDetect, VerbOverlay, VerbSession,
	VerbPing, VerbInfo, VerbShutdown,
}

// isValidVerb checks if a verb is a known command.
func isValidVerb(verb string) bool {
	switch verb {
	case VerbRun, VerbRunJSON, VerbProc, VerbProxy, VerbProxyLog,
		VerbCurrentPage, VerbTunnel, VerbChaos, VerbDetect, VerbOverlay, VerbSession,
		VerbPing, VerbInfo, VerbShutdown:
		return true
	}
	return false
}

// ErrJSONInsteadOfCommand indicates JSON was sent instead of a protocol command.
var ErrJSONInsteadOfCommand = errors.New("json_instead_of_command")

// ErrUnknownCommand indicates an unknown command verb was sent.
type ErrUnknownCommand struct {
	Verb       string
	ValidVerbs []string
}

func (e *ErrUnknownCommand) Error() string {
	return "unknown_command:" + e.Verb
}

// ParseCommand reads and parses a command from the reader.
// It reads until it finds the command terminator ";;".
func (p *Parser) ParseCommand() (*Command, error) {
	// Read until we find the command terminator
	content, err := p.readUntilTerminator(CommandTerminator)
	if err != nil {
		return nil, err
	}

	content = strings.TrimSpace(content)
	if len(content) == 0 {
		return nil, errors.New("empty command")
	}

	// Check for JSON (common misconfiguration)
	if strings.HasPrefix(content, "{") || strings.HasPrefix(content, "[") {
		return nil, ErrJSONInsteadOfCommand
	}

	// Check for data marker "--"
	var cmdPart, dataPart string
	if idx := strings.Index(content, " "+DataMarker+" "); idx != -1 {
		cmdPart = content[:idx]
		dataPart = content[idx+len(" "+DataMarker+" "):]
	} else if strings.HasSuffix(content, " "+DataMarker) {
		// Data marker present but no data (error)
		return nil, errors.New("data marker present but no data length")
	} else {
		cmdPart = content
	}

	// Parse command part
	parts := strings.Fields(cmdPart)
	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}

	verb := strings.ToUpper(parts[0])
	if !isValidVerb(verb) {
		return nil, &ErrUnknownCommand{Verb: verb, ValidVerbs: ValidVerbs}
	}

	cmd := &Command{
		Verb: verb,
	}

	// Parse subverb and args
	if len(parts) > 1 {
		subVerb := strings.ToUpper(parts[1])
		if isSubVerb(subVerb) {
			cmd.SubVerb = subVerb
			cmd.Args = parts[2:]
		} else {
			cmd.Args = parts[1:]
		}
	}

	// Parse data if present (format: "LENGTH\nDATA")
	if dataPart != "" {
		data, err := p.parseDataPart(dataPart)
		if err != nil {
			return nil, fmt.Errorf("failed to parse data: %w", err)
		}
		cmd.Data = data
	}

	return cmd, nil
}

// parseDataPart parses "LENGTH\nBASE64DATA" format
// Data is base64 encoded to safely handle JavaScript, JSON, and binary content
func (p *Parser) parseDataPart(dataPart string) ([]byte, error) {
	// Find the newline separating length from data
	newlineIdx := strings.Index(dataPart, "\n")
	if newlineIdx == -1 {
		return nil, errors.New("data length without data content (missing newline)")
	}

	lengthStr := strings.TrimSpace(dataPart[:newlineIdx])
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return nil, fmt.Errorf("invalid data length %q: %w", lengthStr, err)
	}

	base64Data := dataPart[newlineIdx+1:]

	// Validate length matches actual base64 data
	if len(base64Data) != length {
		return nil, fmt.Errorf("data length mismatch: expected %d, got %d", length, len(base64Data))
	}

	// Decode base64 data
	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 data: %w", err)
	}

	return decoded, nil
}

// readUntilTerminator reads from the reader until the terminator is found.
// Returns the content before the terminator (terminator is consumed but not returned).
func (p *Parser) readUntilTerminator(terminator string) (string, error) {
	var buf bytes.Buffer
	termBytes := []byte(terminator)
	termLen := len(termBytes)

	for {
		b, err := p.reader.ReadByte()
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				return "", fmt.Errorf("unexpected EOF, missing terminator %q", terminator)
			}
			return "", err
		}

		buf.WriteByte(b)

		// Check if buffer ends with terminator
		if buf.Len() >= termLen {
			tail := buf.Bytes()[buf.Len()-termLen:]
			if bytes.Equal(tail, termBytes) {
				// Remove terminator from result
				result := buf.Bytes()[:buf.Len()-termLen]
				return string(result), nil
			}
		}
	}
}

// Resync attempts to resynchronize the parser by scanning for the next terminator.
// This can be used to recover from parse errors.
func (p *Parser) Resync() error {
	_, err := p.readUntilTerminator(CommandTerminator)
	return err
}

// ParseResponse reads and parses a response from the reader.
// Responses also use the ";;" terminator and "--" data marker.
func (p *Parser) ParseResponse() (*Response, error) {
	content, err := p.readUntilTerminator(CommandTerminator)
	if err != nil {
		return nil, err
	}

	content = strings.TrimSpace(content)
	if len(content) == 0 {
		return nil, errors.New("empty response")
	}

	// Check for data marker in response
	var respPart, dataPart string
	if idx := strings.Index(content, " "+DataMarker+" "); idx != -1 {
		respPart = content[:idx]
		dataPart = content[idx+len(" "+DataMarker+" "):]
	} else {
		respPart = content
	}

	parts := strings.SplitN(respPart, " ", 3)
	respType := ResponseType(strings.ToUpper(parts[0]))

	resp := &Response{Type: respType}

	switch respType {
	case ResponseOK:
		if len(parts) > 1 {
			resp.Message = strings.Join(parts[1:], " ")
		}

	case ResponseErr:
		if len(parts) >= 2 {
			resp.Code = parts[1]
		}
		if len(parts) >= 3 {
			resp.Message = parts[2]
		}

	case ResponsePong, ResponseEnd:
		// No additional data

	case ResponseJSON, ResponseData, ResponseChunk:
		if dataPart == "" {
			return nil, fmt.Errorf("%s response requires data", respType)
		}
		data, err := p.parseDataPart(dataPart)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s data: %w", respType, err)
		}
		resp.Data = data

	default:
		return nil, fmt.Errorf("unknown response type: %s", respType)
	}

	return resp, nil
}

// isSubVerb checks if a string is a known sub-verb.
func isSubVerb(s string) bool {
	switch s {
	case SubVerbStatus, SubVerbOutput, SubVerbStop, SubVerbList,
		SubVerbCleanupPort, SubVerbStart, SubVerbExec, SubVerbToast,
		SubVerbQuery, SubVerbClear, SubVerbStats, SubVerbGet, SubVerbSet,
		SubVerbActivity:
		return true
	}
	return false
}

// FormatCommand formats a command for transmission using the resilient format.
// Format: VERB [SUBVERB] [ARGS...] [-- LENGTH\nBASE64DATA];;
// Data is base64 encoded to safely handle JavaScript, JSON, and binary content.
func FormatCommand(cmd *Command) []byte {
	var buf bytes.Buffer

	buf.WriteString(cmd.Verb)
	if cmd.SubVerb != "" {
		buf.WriteByte(' ')
		buf.WriteString(cmd.SubVerb)
	}
	for _, arg := range cmd.Args {
		buf.WriteByte(' ')
		buf.WriteString(arg)
	}

	if len(cmd.Data) > 0 {
		// Base64 encode the data for safe transmission
		encoded := base64.StdEncoding.EncodeToString(cmd.Data)
		buf.WriteByte(' ')
		buf.WriteString(DataMarker)
		buf.WriteByte(' ')
		buf.WriteString(strconv.Itoa(len(encoded)))
		buf.WriteByte('\n')
		buf.WriteString(encoded)
	}

	buf.WriteString(CommandTerminator)
	return buf.Bytes()
}

// Writer provides methods for writing protocol messages.
type Writer struct {
	w io.Writer
}

// NewWriter creates a new protocol writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteOK writes an OK response.
func (w *Writer) WriteOK(message string) error {
	_, err := w.w.Write(FormatOK(message))
	return err
}

// WriteErr writes an error response.
func (w *Writer) WriteErr(code ErrorCode, message string) error {
	_, err := w.w.Write(FormatErr(code, message))
	return err
}

// WritePong writes a PONG response.
func (w *Writer) WritePong() error {
	_, err := w.w.Write(FormatPong())
	return err
}

// WriteJSON writes a JSON response.
func (w *Writer) WriteJSON(data []byte) error {
	_, err := w.w.Write(FormatJSON(data))
	return err
}

// WriteData writes a binary data response.
func (w *Writer) WriteData(data []byte) error {
	_, err := w.w.Write(FormatData(data))
	return err
}

// WriteChunk writes a chunk in a streaming response.
func (w *Writer) WriteChunk(data []byte) error {
	_, err := w.w.Write(FormatChunk(data))
	return err
}

// WriteEnd writes the END marker for chunked responses.
func (w *Writer) WriteEnd() error {
	_, err := w.w.Write(FormatEnd())
	return err
}

// WriteCommand writes a command.
func (w *Writer) WriteCommand(verb string, args []string, data []byte) error {
	cmd := &Command{
		Verb: verb,
		Args: args,
		Data: data,
	}
	_, err := w.w.Write(FormatCommand(cmd))
	return err
}

// WriteCommandWithSubVerb writes a command with a sub-verb.
func (w *Writer) WriteCommandWithSubVerb(verb, subVerb string, args []string, data []byte) error {
	cmd := &Command{
		Verb:    verb,
		SubVerb: subVerb,
		Args:    args,
		Data:    data,
	}
	_, err := w.w.Write(FormatCommand(cmd))
	return err
}

// WriteCommandWithData writes a command with optional data payload.
func (w *Writer) WriteCommandWithData(verb string, args []string, subVerb *string, data []byte) error {
	cmd := &Command{
		Verb: verb,
		Args: args,
		Data: data,
	}
	if subVerb != nil {
		cmd.SubVerb = *subVerb
	}
	_, err := w.w.Write(FormatCommand(cmd))
	return err
}
