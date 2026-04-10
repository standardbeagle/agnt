package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
)

// saveScreenshot saves a base64 data URL to the .agnt/audit directory.
// The file is stored in the project's .agnt/audit folder for easy access by AI agents.
func (ps *ProxyServer) saveScreenshot(name string, dataURL string) (string, error) {
	// Parse data URL (format: data:image/png;base64,...)
	if !strings.HasPrefix(dataURL, "data:") {
		return "", fmt.Errorf("invalid data URL")
	}

	// Find base64 data after comma
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx == -1 {
		return "", fmt.Errorf("invalid data URL format")
	}

	// Decode base64 data
	base64Data := dataURL[commaIdx+1:]
	imageData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	// Get audit directory (.agnt/audit)
	auditDir, err := GetAuditDir()
	if err != nil {
		// Fallback to temp dir if audit directory unavailable
		auditDir = os.TempDir()
	}

	// Create screenshots subdirectory for better organization
	screenshotDir := filepath.Join(auditDir, "screenshots")
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		// Fallback to audit dir root if subdirectory creation fails
		screenshotDir = auditDir
	}

	// Sanitize filename (both ID and name — colons are invalid on Windows)
	safeName := sanitizeFilename(name)
	filename := fmt.Sprintf("screenshot-%s-%s.png", sanitizeFilename(ps.ID), safeName)
	filePath := filepath.Join(screenshotDir, filename)

	// Write to file
	err = os.WriteFile(filePath, imageData, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}

// registerCapture stores a named screenshot file path for cross-goroutine lookup.
// Used only for proxy exec screenshots (__devtool.screenshot('name')).
// Panel screenshots use the per-connection sessionCaptures map instead.
func (ps *ProxyServer) registerCapture(name, filePath string) {
	ps.namedCaptures.Store(name, filePath)
	debug.Log("proxy", "Registered named capture: name=%s path=%s", name, filePath)
}

// LookupCapture returns the file path for a named screenshot, or empty string if not found.
// Entries are deleted after lookup — consumed once by the MCP tool handler.
func (ps *ProxyServer) LookupCapture(name string) string {
	if v, ok := ps.namedCaptures.LoadAndDelete(name); ok {
		return v.(string)
	}
	return ""
}

// savePNGBytes writes raw PNG bytes directly to the screenshots directory.
// This is the fast path for binary WebSocket captures — no base64 decode needed.
func (ps *ProxyServer) savePNGBytes(name string, data []byte) (string, error) {
	auditDir, err := GetAuditDir()
	if err != nil {
		auditDir = os.TempDir()
	}

	screenshotDir := filepath.Join(auditDir, "screenshots")
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		screenshotDir = auditDir
	}

	safeName := sanitizeFilename(name)
	filename := fmt.Sprintf("screenshot-%s-%s.png", sanitizeFilename(ps.ID), safeName)
	filePath := filepath.Join(screenshotDir, filename)

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write screenshot: %w", err)
	}
	return filePath, nil
}

// LargeResultThreshold is the size in bytes above which results are saved to file.
const LargeResultThreshold = 50 * 1024 // 50KB

// saveLargeResult saves a large execution result to a temp file.
// Returns the file path if saved, or empty string if the result was small enough to inline.
func (ps *ProxyServer) saveLargeResult(execID string, result string) (string, error) {
	if len(result) < LargeResultThreshold {
		return "", nil // Small enough to inline
	}

	tempDir := os.TempDir()
	filename := fmt.Sprintf("agnt-result-%s-%s.json", sanitizeFilename(ps.ID), execID)
	filePath := filepath.Join(tempDir, filename)

	err := os.WriteFile(filePath, []byte(result), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write large result: %w", err)
	}

	return filePath, nil
}

// ExecuteJavaScript sends JavaScript code to all connected clients for execution.
// Returns the execution ID and a channel that will receive the result.
func (ps *ProxyServer) ExecuteJavaScript(code string) (string, <-chan *ExecutionResult, error) {
	debug.Log("proxy", "ExecuteJavaScript: proxy=%s code_len=%d", ps.ID, len(code))
	execID := fmt.Sprintf("exec-%d", time.Now().UnixNano())

	// Create result channel for this execution
	resultChan := make(chan *ExecutionResult, 1)
	ps.pendingExecs.Store(execID, resultChan)

	message := map[string]interface{}{
		"type": "execute",
		"id":   execID,
		"code": code,
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		ps.pendingExecs.Delete(execID)
		close(resultChan)
		return "", nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	// Send to all connected clients
	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	if sentCount == 0 {
		debug.Log("proxy", "ExecuteJavaScript: no connected clients for proxy %s", ps.ID)
		ps.pendingExecs.Delete(execID)
		close(resultChan)
		return execID, nil, fmt.Errorf("no connected clients")
	}

	return execID, resultChan, nil
}
