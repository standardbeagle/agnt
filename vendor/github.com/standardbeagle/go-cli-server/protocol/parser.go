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
	"unicode"
)

// Protocol constants for resilient parsing
const (
	// CommandTerminator marks the end of a command
	CommandTerminator = ";;"

	// DataMarker separates arguments from data length
	DataMarker = "--"

	// MaxFrameSize caps how many bytes a single command/response frame may occupy
	// before the terminator is seen. It bounds memory against a peer that opens a
	// connection and never sends ";;".
	MaxFrameSize = 16 << 20 // 16 MiB
)

// PartialFrameError indicates a read error occurred after some bytes of a frame
// were already consumed from the stream. The stream is now desynchronized and
// the connection must be closed — the buffered bytes cannot be recovered, so
// resyncing or a timeout-continue would parse the remainder as garbage.
type PartialFrameError struct {
	Err error // underlying read error (may be a timeout)
}

func (e *PartialFrameError) Error() string {
	return "partial frame, connection desynchronized: " + e.Err.Error()
}

func (e *PartialFrameError) Unwrap() error { return e.Err }

// IsPartialFrame reports whether err is a PartialFrameError.
func IsPartialFrame(err error) bool {
	var pfe *PartialFrameError
	return errors.As(err, &pfe)
}

// ErrFrameTooLarge indicates a frame exceeded MaxFrameSize without a terminator.
var ErrFrameTooLarge = errors.New("frame exceeds maximum size without terminator")

// VerbRegistry tracks registered command verbs for validation.
type VerbRegistry struct {
	mu             sync.RWMutex
	verbs          map[string]bool
	subVerbsByVerb map[string]map[string]bool
}

// NewVerbRegistry creates a new verb registry with built-in verbs.
func NewVerbRegistry() *VerbRegistry {
	vr := &VerbRegistry{
		verbs:          make(map[string]bool),
		subVerbsByVerb: make(map[string]map[string]bool),
	}
	// Register built-in verbs. VerbScript was omitted, so every SCRIPT command was
	// rejected at parse validation despite the hub registering a handler for it.
	vr.RegisterVerb(VerbRun, VerbRunJSON, VerbProc,
		VerbSession, VerbSubprocess, VerbScript, VerbPing, VerbInfo, VerbShutdown)
	// Register built-in sub-verbs scoped to their verbs. A global sub-verb set
	// makes commands like "RUN start build.sh" eat "start" as a sub-verb.
	vr.RegisterSubVerbForVerb(VerbProc, SubVerbStatus, SubVerbOutput, SubVerbStop,
		SubVerbList, SubVerbCleanupPort, SubVerbStdin, SubVerbStream)
	vr.RegisterSubVerbForVerb(VerbSubprocess, SubVerbRegister, SubVerbUnregister,
		SubVerbHeartbeat, SubVerbList, SubVerbStatus)
	vr.RegisterSubVerbForVerb(VerbSession, SubVerbRegister, SubVerbUnregister,
		SubVerbHeartbeat, SubVerbGet, SubVerbList)
	vr.RegisterSubVerbForVerb(VerbScript, SubVerbList, SubVerbGet, SubVerbSet,
		SubVerbClear, SubVerbRestart)
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

// RegisterSubVerbForVerb adds sub-verbs that are valid only for the given verb.
func (vr *VerbRegistry) RegisterSubVerbForVerb(verb string, subVerbs ...string) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	verb = strings.ToUpper(verb)
	if vr.subVerbsByVerb[verb] == nil {
		vr.subVerbsByVerb[verb] = make(map[string]bool)
	}
	for _, sv := range subVerbs {
		vr.subVerbsByVerb[verb][strings.ToUpper(sv)] = true
	}
}

// IsValidVerb checks if a verb is registered.
func (vr *VerbRegistry) IsValidVerb(verb string) bool {
	vr.mu.RLock()
	defer vr.mu.RUnlock()
	return vr.verbs[strings.ToUpper(verb)]
}

// IsSubVerbForVerb checks whether subVerb is registered for verb.
func (vr *VerbRegistry) IsSubVerbForVerb(verb, subVerb string) bool {
	vr.mu.RLock()
	defer vr.mu.RUnlock()
	subs := vr.subVerbsByVerb[strings.ToUpper(verb)]
	if subs == nil {
		return false
	}
	return subs[strings.ToUpper(subVerb)]
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
		if p.registry.IsSubVerbForVerb(verb, subVerb) {
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
			// A read error after partial data means the stream is desynchronized:
			// the consumed bytes are gone from the reader and cannot be replayed.
			// Signal a fatal, non-recoverable condition so the caller closes rather
			// than continuing (which would parse the remainder as a new frame).
			if buf.Len() > 0 {
				if err == io.EOF {
					err = fmt.Errorf("unexpected EOF, missing terminator %q: %w", terminator, err)
				}
				return "", &PartialFrameError{Err: err}
			}
			return "", err
		}

		buf.WriteByte(b)

		if buf.Len() > MaxFrameSize {
			return "", &PartialFrameError{Err: ErrFrameTooLarge}
		}

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

	case ResponseJSON, ResponseData, ResponseChunk, ResponseStatus:
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

// ValidateCommand rejects commands that would corrupt the wire format. The
// protocol is whitespace-delimited and terminated by ";;", so a token containing
// the terminator injects a second command, a newline breaks the frame, and a lone
// "--" is misread as the data marker. Those are rejected in every token. An arg
// carries a user-supplied value, so it must additionally be free of whitespace
// that would silently split it into extra args; the verb/sub-verb are developer
// constants (and may be a pre-joined command line the receiver re-splits), so
// interior spaces there are allowed. Data payloads are exempt — base64-encoded.
func ValidateCommand(cmd *Command) error {
	if err := validateToken("verb", cmd.Verb, true); err != nil {
		return err
	}
	if cmd.SubVerb != "" {
		if err := validateToken("sub-verb", cmd.SubVerb, true); err != nil {
			return err
		}
	}
	for _, arg := range cmd.Args {
		if err := validateToken("arg", arg, false); err != nil {
			return err
		}
	}
	return nil
}

// validateToken ensures a single wire token is safe to emit unescaped.
func validateToken(kind, tok string, allowSpace bool) error {
	if tok == "" {
		return fmt.Errorf("%s cannot be empty", kind)
	}
	if strings.Contains(tok, CommandTerminator) {
		return fmt.Errorf("%s %q contains terminator %q", kind, tok, CommandTerminator)
	}
	if strings.ContainsAny(tok, "\r\n") {
		return fmt.Errorf("%s %q contains a newline", kind, tok)
	}
	for _, field := range strings.Fields(tok) {
		if field == DataMarker {
			return fmt.Errorf("%s cannot contain the data marker %q", kind, DataMarker)
		}
	}
	if !allowSpace {
		for _, r := range tok {
			if unicode.IsSpace(r) {
				return fmt.Errorf("%s %q contains whitespace", kind, tok)
			}
		}
	}
	return nil
}

func validateFrameSize(frame []byte) error {
	if len(frame) > MaxFrameSize {
		return ErrFrameTooLarge
	}
	return nil
}

// ValidateFrameSize reports whether a raw protocol frame is small enough to
// send. It is intended for callers that deliberately bypass Writer formatting
// (for example a raw CLI command) but still need the same sender-side guard.
func ValidateFrameSize(frame []byte) error {
	return validateFrameSize(frame)
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
	frame := FormatOK(message)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteErr writes an error response.
func (w *Writer) WriteErr(code ErrorCode, message string) error {
	frame := FormatErr(code, message)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WritePong writes a PONG response.
func (w *Writer) WritePong() error {
	frame := FormatPong()
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteJSON writes a JSON response.
func (w *Writer) WriteJSON(data []byte) error {
	frame := FormatJSON(data)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteData writes a binary data response.
func (w *Writer) WriteData(data []byte) error {
	frame := FormatData(data)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteChunk writes a chunk in a streaming response.
func (w *Writer) WriteChunk(data []byte) error {
	frame := FormatChunk(data)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteStatus writes an out-of-band progress/liveness frame.
func (w *Writer) WriteStatus(data []byte) error {
	frame := FormatStatus(data)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteEnd writes the END marker for chunked responses.
func (w *Writer) WriteEnd() error {
	frame := FormatEnd()
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}

// WriteCommand writes a command.
func (w *Writer) WriteCommand(verb string, args []string, data []byte) error {
	cmd := &Command{
		Verb: verb,
		Args: args,
		Data: data,
	}
	if err := ValidateCommand(cmd); err != nil {
		return err
	}
	frame := FormatCommand(cmd)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
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
	if err := ValidateCommand(cmd); err != nil {
		return err
	}
	frame := FormatCommand(cmd)
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	_, err := w.w.Write(frame)
	return err
}
