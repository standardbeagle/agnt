package claude

import (
	"encoding/json"
	"fmt"
)

// ClaudeSDKError is the base error type for all Claude SDK errors.
type ClaudeSDKError struct {
	Message string
	Cause   error
}

func (e *ClaudeSDKError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *ClaudeSDKError) Unwrap() error {
	return e.Cause
}

// NewClaudeSDKError creates a new ClaudeSDKError.
func NewClaudeSDKError(message string, cause error) *ClaudeSDKError {
	return &ClaudeSDKError{
		Message: message,
		Cause:   cause,
	}
}

// CLIConnectionError is raised when unable to connect to Claude Code.
type CLIConnectionError struct {
	ClaudeSDKError
}

// NewCLIConnectionError creates a new CLIConnectionError.
func NewCLIConnectionError(message string, cause error) *CLIConnectionError {
	return &CLIConnectionError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
	}
}

// CLINotFoundError is raised when Claude Code is not found or not installed.
type CLINotFoundError struct {
	CLIConnectionError
	CLIPath string
}

func (e *CLINotFoundError) Error() string {
	msg := e.Message
	if e.CLIPath != "" {
		msg = fmt.Sprintf("%s (searched: %s)", msg, e.CLIPath)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// NewCLINotFoundError creates a new CLINotFoundError.
func NewCLINotFoundError(message string, cliPath string) *CLINotFoundError {
	return &CLINotFoundError{
		CLIConnectionError: CLIConnectionError{
			ClaudeSDKError: ClaudeSDKError{
				Message: message,
			},
		},
		CLIPath: cliPath,
	}
}

// ProcessError is raised when the CLI process fails.
type ProcessError struct {
	ClaudeSDKError
	ExitCode int
	Stderr   string
}

func (e *ProcessError) Error() string {
	msg := e.Message
	if e.ExitCode != 0 {
		msg = fmt.Sprintf("%s (exit code: %d)", msg, e.ExitCode)
	}
	if e.Stderr != "" {
		msg = fmt.Sprintf("%s\nstderr: %s", msg, e.Stderr)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// NewProcessError creates a new ProcessError.
func NewProcessError(message string, exitCode int, stderr string, cause error) *ProcessError {
	return &ProcessError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
		ExitCode: exitCode,
		Stderr:   stderr,
	}
}

// CLIJSONDecodeError is raised when unable to decode JSON from CLI output.
type CLIJSONDecodeError struct {
	ClaudeSDKError
	Line string
}

func (e *CLIJSONDecodeError) Error() string {
	msg := e.Message
	if e.Line != "" {
		// Truncate long lines
		line := e.Line
		if len(line) > 100 {
			line = line[:100] + "..."
		}
		msg = fmt.Sprintf("%s (line: %s)", msg, line)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// NewCLIJSONDecodeError creates a new CLIJSONDecodeError.
func NewCLIJSONDecodeError(message string, line string, cause error) *CLIJSONDecodeError {
	return &CLIJSONDecodeError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
		Line: line,
	}
}

// MessageParseError is raised when unable to parse a message from CLI output.
type MessageParseError struct {
	ClaudeSDKError
	Data json.RawMessage
}

func (e *MessageParseError) Error() string {
	msg := e.Message
	if len(e.Data) > 0 {
		// Truncate long data
		data := string(e.Data)
		if len(data) > 100 {
			data = data[:100] + "..."
		}
		msg = fmt.Sprintf("%s (data: %s)", msg, data)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// NewMessageParseError creates a new MessageParseError.
func NewMessageParseError(message string, data json.RawMessage, cause error) *MessageParseError {
	return &MessageParseError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
		Data: data,
	}
}

// HookError is raised when a hook fails to execute.
type HookError struct {
	ClaudeSDKError
	HookEvent string
	ToolName  string
}

func (e *HookError) Error() string {
	msg := e.Message
	if e.HookEvent != "" {
		msg = fmt.Sprintf("[%s] %s", e.HookEvent, msg)
	}
	if e.ToolName != "" {
		msg = fmt.Sprintf("%s (tool: %s)", msg, e.ToolName)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// NewHookError creates a new HookError.
func NewHookError(message string, hookEvent string, toolName string, cause error) *HookError {
	return &HookError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
		HookEvent: hookEvent,
		ToolName:  toolName,
	}
}

// SessionClosedError is raised when trying to use a closed session.
type SessionClosedError struct {
	ClaudeSDKError
	SessionID string
}

func (e *SessionClosedError) Error() string {
	msg := e.Message
	if e.SessionID != "" {
		msg = fmt.Sprintf("%s (session: %s)", msg, e.SessionID)
	}
	return msg
}

// NewSessionClosedError creates a new SessionClosedError.
func NewSessionClosedError(sessionID string) *SessionClosedError {
	return &SessionClosedError{
		ClaudeSDKError: ClaudeSDKError{
			Message: "session is closed",
		},
		SessionID: sessionID,
	}
}

// ToolError is raised when a tool execution fails.
type ToolError struct {
	ClaudeSDKError
	ToolName  string
	ToolUseID string
}

func (e *ToolError) Error() string {
	msg := e.Message
	if e.ToolName != "" {
		msg = fmt.Sprintf("[%s] %s", e.ToolName, msg)
	}
	if e.ToolUseID != "" {
		msg = fmt.Sprintf("%s (id: %s)", msg, e.ToolUseID)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

// NewToolError creates a new ToolError.
func NewToolError(message string, toolName string, toolUseID string, cause error) *ToolError {
	return &ToolError{
		ClaudeSDKError: ClaudeSDKError{
			Message: message,
			Cause:   cause,
		},
		ToolName:  toolName,
		ToolUseID: toolUseID,
	}
}

// Error type checking helpers.

// IsCLINotFound checks if the error is a CLINotFoundError.
func IsCLINotFound(err error) bool {
	_, ok := err.(*CLINotFoundError)
	return ok
}

// IsCLIConnectionError checks if the error is a CLIConnectionError.
func IsCLIConnectionError(err error) bool {
	_, ok := err.(*CLIConnectionError)
	if ok {
		return true
	}
	// Also check for CLINotFoundError which embeds CLIConnectionError
	_, ok = err.(*CLINotFoundError)
	return ok
}

// IsProcessError checks if the error is a ProcessError.
func IsProcessError(err error) bool {
	_, ok := err.(*ProcessError)
	return ok
}

// IsJSONDecodeError checks if the error is a CLIJSONDecodeError.
func IsJSONDecodeError(err error) bool {
	_, ok := err.(*CLIJSONDecodeError)
	return ok
}

// IsMessageParseError checks if the error is a MessageParseError.
func IsMessageParseError(err error) bool {
	_, ok := err.(*MessageParseError)
	return ok
}

// IsSessionClosedError checks if the error is a SessionClosedError.
func IsSessionClosedError(err error) bool {
	_, ok := err.(*SessionClosedError)
	return ok
}

// IsToolError checks if the error is a ToolError.
func IsToolError(err error) bool {
	_, ok := err.(*ToolError)
	return ok
}

// IsHookError checks if the error is a HookError.
func IsHookError(err error) bool {
	_, ok := err.(*HookError)
	return ok
}
