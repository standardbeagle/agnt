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
	"sync"
)

// Protocol constants for resilient parsing
const (
	// CommandTerminator marks the end of a command
	CommandTerminator = ";;"

	// DataMarker separates arguments from data length
	DataMarker = "--"
)

// VerbRegistry tracks registered command verbs for validation.
type VerbRegistry struct {
	mu       sync.RWMutex
	verbs    map[string]bool
	subVerbs map[string]bool
}

// NewVerbRegistry creates a new verb registry with built-in verbs.
func NewVerbRegistry() *VerbRegistry {
	vr := &VerbRegistry{
		verbs:    make(map[string]bool),
		subVerbs: make(map[string]bool),
	}
	// Register built-in verbs
	vr.RegisterVerb(VerbRun, VerbRunJSON, VerbProc, VerbRelay, VerbAttach, VerbDetach,
		VerbSession, VerbSubprocess, VerbPing, VerbInfo, VerbShutdown)
	// Register built-in sub-verbs
	vr.RegisterSubVerb(SubVerbStatus, SubVerbOutput, SubVerbStop, SubVerbList,
		SubVerbCleanupPort, SubVerbStdin, SubVerbStream, SubVerbSend, SubVerbBroadcast,
		SubVerbRequest, SubVerbRegister, SubVerbUnregister, SubVerbHeartbeat,
		SubVerbGet, SubVerbStart, SubVerbClear, SubVerbSet)
	return vr
}

// RegisterVerb adds verbs to the registry.
func (vr *VerbRegistry) RegisterVerb(verbs ...string) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	for _, v := range verbs {
		vr.verbs[strings.ToUpper(v)] = true
	}
}

// RegisterSubVerb adds sub-verbs to the registry.
func (vr *VerbRegistry) RegisterSubVerb(subVerbs ...string) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	for _, sv := range subVerbs {
		vr.subVerbs[strings.ToUpper(sv)] = true
	}
}

// IsValidVerb checks if a verb is registered.
func (vr *VerbRegistry) IsValidVerb(verb string) bool {
	vr.mu.RLock()
	defer vr.mu.RUnlock()
	return vr.verbs[strings.ToUpper(verb)]
}

// IsSubVerb checks if a string is a registered sub-verb.
func (vr *VerbRegistry) IsSubVerb(s string) bool {
	vr.mu.RLock()
	defer vr.mu.RUnlock()
	return vr.subVerbs[strings.ToUpper(s)]
}

// ValidVerbs returns a list of all registered verbs.
func (vr *VerbRegistry) ValidVerbs() []string {
	vr.mu.RLock()
	defer vr.mu.RUnlock()
	result := make([]string, 0, len(vr.verbs))
	for v := range vr.verbs {
		result = append(result, v)
	}
	return result
}

// DefaultRegistry is the global verb registry.
var DefaultRegistry = NewVerbRegistry()

// Parser handles parsing of protocol commands and responses.
//
// Commands use explicit terminators for resilience:
//   - Commands end with ";;"
//   - Data is indicated by "--" followed by length
//
// Format:
//
//	VERB [SUBVERB] [ARGS...] [-- LENGTH\nDATA];;
type Parser struct {
	reader   *bufio.Reader
	registry *VerbRegistry
}

// NewParser creates a new protocol parser with the default registry.
func NewParser(r io.Reader) *Parser {
	return &Parser{
		reader:   bufio.NewReader(r),
		registry: DefaultRegistry,
	}
}

// NewParserWithRegistry creates a parser with a custom verb registry.
func NewParserWithRegistry(r io.Reader, registry *VerbRegistry) *Parser {
	return &Parser{
		reader:   bufio.NewReader(r),
		registry: registry,
	}
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
func (p *Parser) ParseCommand() (*Command, error) {
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
	if !p.registry.IsValidVerb(verb) {
		return nil, &ErrUnknownCommand{Verb: verb, ValidVerbs: p.registry.ValidVerbs()}
	}

	cmd := &Command{
		Verb: verb,
	}

	// Parse subverb and args
	if len(parts) > 1 {
		subVerb := strings.ToUpper(parts[1])
		if p.registry.IsSubVerb(subVerb) {
			cmd.SubVerb = subVerb
			cmd.Args = parts[2:]
		} else {
			cmd.Args = parts[1:]
		}
	}

	// Parse data if present
	if dataPart != "" {
		data, err := p.parseDataPart(dataPart)
		if err != nil {
			return nil, fmt.Errorf("failed to parse data: %w", err)
		}
		cmd.Data = data
	}

	return cmd, nil
}

// parseDataPart parses "LENGTH\nBASE64DATA" format.
// Handles both \n and \r\n line endings for Windows compatibility.
func (p *Parser) parseDataPart(dataPart string) ([]byte, error) {
	newlineIdx := strings.Index(dataPart, "\n")
	if newlineIdx == -1 {
		// No newline — only valid if length is 0 (empty data, newline was trailing and got trimmed)
		lengthStr := strings.TrimSpace(dataPart)
		if lengthStr == "0" {
			return []byte{}, nil
		}
		return nil, fmt.Errorf("data length without data content (missing newline): got %q", dataPart)
	}

	lengthStr := strings.TrimSpace(dataPart[:newlineIdx])
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return nil, fmt.Errorf("invalid data length %q: %w", lengthStr, err)
	}

	base64Data := dataPart[newlineIdx+1:]
	// Strip trailing \r that may be left from \r\n line endings on Windows
	base64Data = strings.TrimRight(base64Data, "\r")

	if length == 0 {
		return []byte{}, nil
	}

	if len(base64Data) != length {
		return nil, fmt.Errorf("data length mismatch: expected %d, got %d (data: %q)", length, len(base64Data), truncate(base64Data, 50))
	}

	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 data: %w", err)
	}

	return decoded, nil
}

// truncate returns a string truncated to maxLen with "..." suffix if needed.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// readUntilTerminator reads from the reader until the terminator is found.
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

		if buf.Len() >= termLen {
			tail := buf.Bytes()[buf.Len()-termLen:]
			if bytes.Equal(tail, termBytes) {
				result := buf.Bytes()[:buf.Len()-termLen]
				return string(result), nil
			}
		}
	}
}

// Resync attempts to resynchronize by scanning for the next terminator.
func (p *Parser) Resync() error {
	_, err := p.readUntilTerminator(CommandTerminator)
	return err
}

// ParseResponse reads and parses a response from the reader.
func (p *Parser) ParseResponse() (*Response, error) {
	content, err := p.readUntilTerminator(CommandTerminator)
	if err != nil {
		return nil, err
	}

	content = strings.TrimSpace(content)
	if len(content) == 0 {
		return nil, errors.New("empty response")
	}

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

// FormatCommand formats a command for transmission.
// Format: VERB [SUBVERB] [ARGS...] [-- LENGTH\nBASE64DATA];;
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
// The subVerb parameter is optional (pass nil if not needed).
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
