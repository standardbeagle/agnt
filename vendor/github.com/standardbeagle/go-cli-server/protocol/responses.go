package protocol

import (
	"encoding/base64"
	"fmt"
	"strconv"
)

// ResponseType indicates the type of response.
type ResponseType string

const (
	ResponseOK    ResponseType = "OK"
	ResponseErr   ResponseType = "ERR"
	ResponseData  ResponseType = "DATA"
	ResponseJSON  ResponseType = "JSON"
	ResponseChunk ResponseType = "CHUNK"
	ResponseEnd   ResponseType = "END"
	ResponsePong  ResponseType = "PONG"
)

// Response represents a response from the hub.
type Response struct {
	Type    ResponseType
	Message string // For OK/ERR responses
	Code    string // Error code for ERR responses
	Data    []byte // Binary/JSON data for DATA/JSON/CHUNK responses
}

// ErrorCode represents hub error codes.
type ErrorCode string

const (
	ErrNotFound       ErrorCode = "not_found"
	ErrAlreadyExists  ErrorCode = "already_exists"
	ErrInvalidState   ErrorCode = "invalid_state"
	ErrShuttingDown   ErrorCode = "shutting_down"
	ErrPortInUse      ErrorCode = "port_in_use"
	ErrInvalidArgs    ErrorCode = "invalid_args"
	ErrInvalidAction  ErrorCode = "invalid_action"
	ErrInvalidCommand ErrorCode = "invalid_command"
	ErrMissingParam   ErrorCode = "missing_param"
	ErrTimeout        ErrorCode = "timeout"
	ErrInternal       ErrorCode = "internal"
	ErrNotAttached    ErrorCode = "not_attached"    // Process not attached to hub
	ErrDeliveryFailed ErrorCode = "delivery_failed" // Message delivery failed
)

// StructuredError contains programmatic error details.
type StructuredError struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	Command      string    `json:"command,omitempty"`
	Action       string    `json:"action,omitempty"`
	ValidActions []string  `json:"valid_actions,omitempty"`
	Param        string    `json:"param,omitempty"`
	ValidParams  []string  `json:"valid_params,omitempty"`
}

// Error builders - centralize structured error construction to reduce shotgun surgery

// NewMissingParamError creates a structured error for missing required parameters.
func NewMissingParamError(command, param, message string) *StructuredError {
	if message == "" {
		message = fmt.Sprintf("missing required parameter: %s", param)
	}
	return &StructuredError{
		Code:    ErrMissingParam,
		Message: message,
		Command: command,
		Param:   param,
	}
}

// NewInvalidActionError creates a structured error for invalid actions.
func NewInvalidActionError(command, action string, validActions []string) *StructuredError {
	return &StructuredError{
		Code:         ErrInvalidAction,
		Message:      fmt.Sprintf("invalid action: %s", action),
		Command:      command,
		Action:       action,
		ValidActions: validActions,
	}
}

// NewInternalError creates a structured error for internal errors.
func NewInternalError(message string) *StructuredError {
	return &StructuredError{
		Code:    ErrInternal,
		Message: message,
	}
}

// NewNotFoundError creates a structured error for not found resources.
func NewNotFoundError(resource, id string) *StructuredError {
	return &StructuredError{
		Code:    ErrNotFound,
		Message: fmt.Sprintf("%s not found: %s", resource, id),
	}
}

// NewInvalidArgsError creates a structured error for invalid arguments.
func NewInvalidArgsError(message string) *StructuredError {
	return &StructuredError{
		Code:    ErrInvalidArgs,
		Message: message,
	}
}

// FormatOK formats a simple OK response.
// Format: OK [message];;
func FormatOK(message string) []byte {
	if message == "" {
		return []byte("OK" + CommandTerminator)
	}
	return []byte(fmt.Sprintf("OK %s%s", message, CommandTerminator))
}

// FormatErr formats an error response.
// Format: ERR code message;;
func FormatErr(code ErrorCode, message string) []byte {
	return []byte(fmt.Sprintf("ERR %s %s%s", code, message, CommandTerminator))
}

// FormatPong formats a PONG response.
// Format: PONG;;
func FormatPong() []byte {
	return []byte("PONG" + CommandTerminator)
}

// FormatJSON formats a JSON response with base64 encoded data.
// Format: JSON -- LENGTH\nBASE64DATA;;
func FormatJSON(data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	return []byte(fmt.Sprintf("JSON %s %d\n%s%s", DataMarker, len(encoded), encoded, CommandTerminator))
}

// FormatData formats a binary data response with base64 encoding.
// Format: DATA -- LENGTH\nBASE64DATA;;
func FormatData(data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	return []byte(fmt.Sprintf("DATA %s %d\n%s%s", DataMarker, len(encoded), encoded, CommandTerminator))
}

// FormatChunk formats a streaming chunk with base64 encoding.
// Format: CHUNK -- LENGTH\nBASE64DATA;;
func FormatChunk(data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	return []byte(fmt.Sprintf("CHUNK %s %d\n%s%s", DataMarker, len(encoded), encoded, CommandTerminator))
}

// FormatEnd formats an END response for chunked streams.
// Format: END;;
func FormatEnd() []byte {
	return []byte("END" + CommandTerminator)
}

// ParseLengthPrefixed parses a length-prefixed response line.
func ParseLengthPrefixed(line string, prefix string) (int, error) {
	if len(line) <= len(prefix)+1 {
		return 0, fmt.Errorf("invalid %s response: too short", prefix)
	}

	lengthStr := line[len(prefix)+1:]
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return 0, fmt.Errorf("invalid %s length: %w", prefix, err)
	}

	return length, nil
}
