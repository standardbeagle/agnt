package tools

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/standardbeagle/agnt/internal/debug"

	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/protocol"

	"time"
)

// getProjectPath returns the project path for filtering processes/proxies.
// It first checks for AGNT_PROJECT_PATH environment variable (set by agnt run),
// then falls back to the current working directory.
// On Windows, paths are normalized to lowercase for case-insensitive comparison.
func getProjectPath() string {
	var path string

	if envPath := os.Getenv("AGNT_PROJECT_PATH"); envPath != "" {

		absPath, err := filepath.Abs(envPath)
		if err == nil {
			path = absPath
		} else {
			path = envPath
		}
	} else {

		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		path = cwd
	}

	if isWindows() {
		path = strings.ToLower(path)
	}
	return path
}

// isWindows returns true if running on Windows.
func isWindows() bool {
	return os.PathSeparator == '\\'
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func getInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

func getFloat64(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func getStringSlice(m map[string]interface{}, key string) []string {
	arr, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func getTime(m map[string]interface{}, key string) time.Time {
	if v, ok := m[key].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// formatDaemonError parses structured errors from daemon and creates helpful LLM-friendly messages.
func formatDaemonError(err error, toolName string) *mcp.CallToolResult {
	debug.Log("tools", "daemon error in %s: %v", toolName, err)
	errStr := err.Error()

	if idx := strings.Index(errStr, "] {"); idx != -1 {
		jsonStart := idx + 2
		jsonStr := errStr[jsonStart:]

		var structErr protocol.StructuredError
		if json.Unmarshal([]byte(jsonStr), &structErr) == nil {
			return formatStructuredError(&structErr, toolName)
		}
	}

	return errorResult(fmt.Sprintf("%s failed: %v", toolName, err))
}

// formatStructuredError creates a helpful LLM-friendly message from a structured error.
func formatStructuredError(err *protocol.StructuredError, toolName string) *mcp.CallToolResult {
	var msg strings.Builder

	switch err.Code {
	case protocol.ErrInvalidAction:
		msg.WriteString(fmt.Sprintf("%s: unknown action %q", toolName, err.Action))
		if len(err.ValidActions) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid actions: %s", strings.Join(err.ValidActions, ", ")))
			msg.WriteString("\n\nExamples:\n")
			for _, action := range err.ValidActions {
				msg.WriteString(fmt.Sprintf("  %s {action: %q, ...}\n", toolName, strings.ToLower(action)))
			}
		}

	case protocol.ErrMissingParam:
		msg.WriteString(fmt.Sprintf("%s: %s is required", toolName, err.Param))
		if len(err.ValidActions) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid values for %s: %s", err.Param, strings.Join(err.ValidActions, ", ")))
		}
		if len(err.ValidParams) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid values: %s", strings.Join(err.ValidParams, ", ")))
		}

	case protocol.ErrInvalidCommand:
		msg.WriteString(fmt.Sprintf("unknown command %q", err.Command))
		if len(err.ValidActions) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid commands: %s", strings.Join(err.ValidActions, ", ")))
		}

	case protocol.ErrNotFound:
		msg.WriteString(fmt.Sprintf("%s: not found - %s", toolName, err.Message))

	default:
		msg.WriteString(fmt.Sprintf("%s: %s", toolName, err.Message))
	}

	return errorResult(msg.String())
}

// normalizeAddr replaces 0.0.0.0 or [::] with localhost for display.
func normalizeAddr(addr string) string {
	addr = strings.Replace(addr, "0.0.0.0", "localhost", 1)
	addr = strings.Replace(addr, "[::]", "localhost", 1)
	return addr
}
