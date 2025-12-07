package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Parser handles parsing of protocol commands and responses.
type Parser struct {
	reader *bufio.Reader
}

// NewParser creates a new protocol parser.
func NewParser(r io.Reader) *Parser {
	return &Parser{
		reader: bufio.NewReader(r),
	}
}

// ParseCommand reads and parses a command from the reader.
func (p *Parser) ParseCommand() (*Command, error) {
	line, err := p.readLine()
	if err != nil {
		return nil, err
	}

	if len(line) == 0 {
		return nil, errors.New("empty command")
	}

	parts := strings.Fields(line)
	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}

	cmd := &Command{
		Verb: strings.ToUpper(parts[0]),
	}

	// Handle commands with data payloads
	if cmd.Verb == VerbRunJSON || strings.HasSuffix(line, "\\") {
		// RUN-JSON <length>\r\n<data>\r\n
		return p.parseCommandWithData(cmd, parts)
	}

	// Parse remaining parts
	if len(parts) > 1 {
		// Check if second part is a known sub-verb
		subVerb := strings.ToUpper(parts[1])
		if isSubVerb(subVerb) {
			cmd.SubVerb = subVerb
			cmd.Args = parts[2:]
		} else {
			cmd.Args = parts[1:]
		}
	}

	// Check for commands that have inline data with length prefix
	// e.g., PROXY EXEC <id> <length>\r\n<code>\r\n
	// e.g., PROXYLOG QUERY <proxy_id> <length>\r\n<filter>\r\n
	if needsDataPayload(cmd) {
		return p.parseInlineDataPayload(cmd)
	}

	return cmd, nil
}

// parseCommandWithData handles commands that start with a length prefix.
func (p *Parser) parseCommandWithData(cmd *Command, parts []string) (*Command, error) {
	if len(parts) < 2 {
		return nil, fmt.Errorf("%s requires length parameter", cmd.Verb)
	}

	length, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid length for %s: %w", cmd.Verb, err)
	}

	data, err := p.readExactly(length)
	if err != nil {
		return nil, fmt.Errorf("failed to read data for %s: %w", cmd.Verb, err)
	}

	// Read trailing \r\n
	if _, err := p.readLine(); err != nil {
		return nil, fmt.Errorf("missing trailing CRLF for %s: %w", cmd.Verb, err)
	}

	cmd.Data = data
	return cmd, nil
}

// parseInlineDataPayload handles commands where the last argument is a length.
func (p *Parser) parseInlineDataPayload(cmd *Command) (*Command, error) {
	if len(cmd.Args) == 0 {
		return nil, fmt.Errorf("%s %s requires arguments", cmd.Verb, cmd.SubVerb)
	}

	// Last argument should be the length
	lastArg := cmd.Args[len(cmd.Args)-1]
	length, err := strconv.Atoi(lastArg)
	if err != nil {
		// No inline data payload
		return cmd, nil
	}

	// Remove length from args
	cmd.Args = cmd.Args[:len(cmd.Args)-1]

	// Read the data payload
	data, err := p.readExactly(length)
	if err != nil {
		return nil, fmt.Errorf("failed to read inline data: %w", err)
	}

	// Read trailing \r\n
	if _, err := p.readLine(); err != nil {
		return nil, fmt.Errorf("missing trailing CRLF: %w", err)
	}

	cmd.Data = data
	return cmd, nil
}

// ParseResponse reads and parses a response from the reader.
func (p *Parser) ParseResponse() (*Response, error) {
	line, err := p.readLine()
	if err != nil {
		return nil, err
	}

	if len(line) == 0 {
		return nil, errors.New("empty response")
	}

	parts := strings.SplitN(line, " ", 3)
	respType := ResponseType(strings.ToUpper(parts[0]))

	switch respType {
	case ResponseOK:
		resp := &Response{Type: ResponseOK}
		if len(parts) > 1 {
			resp.Message = strings.Join(parts[1:], " ")
		}
		return resp, nil

	case ResponseErr:
		resp := &Response{Type: ResponseErr}
		if len(parts) >= 2 {
			resp.Code = parts[1]
		}
		if len(parts) >= 3 {
			resp.Message = parts[2]
		}
		return resp, nil

	case ResponsePong:
		return &Response{Type: ResponsePong}, nil

	case ResponseEnd:
		return &Response{Type: ResponseEnd}, nil

	case ResponseJSON, ResponseData, ResponseChunk:
		if len(parts) < 2 {
			return nil, fmt.Errorf("missing length for %s response", respType)
		}
		length, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid length for %s: %w", respType, err)
		}

		data, err := p.readExactly(length)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s data: %w", respType, err)
		}

		// Read trailing \r\n
		if _, err := p.readLine(); err != nil {
			return nil, fmt.Errorf("missing trailing CRLF for %s: %w", respType, err)
		}

		return &Response{Type: respType, Data: data}, nil

	default:
		return nil, fmt.Errorf("unknown response type: %s", respType)
	}
}

// readLine reads a line terminated by \r\n or \n.
func (p *Parser) readLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	// Trim trailing \r\n or \n
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")

	return line, nil
}

// readExactly reads exactly n bytes from the reader.
func (p *Parser) readExactly(n int) ([]byte, error) {
	data := make([]byte, n)
	_, err := io.ReadFull(p.reader, data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// isSubVerb checks if a string is a known sub-verb.
func isSubVerb(s string) bool {
	switch s {
	case SubVerbStatus, SubVerbOutput, SubVerbStop, SubVerbList,
		SubVerbCleanupPort, SubVerbStart, SubVerbExec, SubVerbQuery,
		SubVerbClear, SubVerbStats, SubVerbGet:
		return true
	}
	return false
}

// needsDataPayload checks if a command might have an inline data payload.
func needsDataPayload(cmd *Command) bool {
	switch cmd.Verb {
	case VerbProxy:
		return cmd.SubVerb == SubVerbExec
	case VerbProxyLog:
		return cmd.SubVerb == SubVerbQuery
	}
	return false
}

// FormatCommand formats a command for transmission.
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
		buf.WriteByte(' ')
		buf.WriteString(strconv.Itoa(len(cmd.Data)))
		buf.WriteString("\r\n")
		buf.Write(cmd.Data)
	}

	buf.WriteString("\r\n")
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
