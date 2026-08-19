package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// execResultTimeout bounds how long a pending JavaScript execution waits for a
// browser reply before its pendingExecs entry is reaped, preventing a leak when
// the page navigates/reloads mid-exec and no reply ever arrives.
const execResultTimeout = 60 * time.Second

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

	// Get audit directory (<project root>/.agnt/audit). ps.Path is the working
	// directory the proxy was created for; the daemon's own cwd is unrelated to
	// it and would put this project's screenshots in another project's tree.
	auditDir, err := GetAuditDir(ps.Path)
	if err != nil {
		// DELIBERATE exemption from the Silent Failure Prohibition
		// (.claude/rules/daemon-architecture.md § Silent Failure Prohibition).
		// GetAuditDir fails LOUD when the project root is missing; here we
		// intentionally catch that and fall back to os.TempDir() rather than
		// propagate the error, for three reasons:
		//   1. Data-preservation > loud-fail on THIS path: losing a screenshot
		//      outright is worse than writing it somewhere less convenient.
		//   2. A missing project root is legitimate for some proxy shapes, so
		//      this is a "sometimes-expected" condition — surfacing it to the
		//      agent/incident path on every fallback would flood the queue
		//      (anti-spam; cf. § toast-vs-agent-surfaces / messaging-queue).
		//   3. This is NOT a harmful silent failure: the full temp-dir path is
		//      RETURNED to the caller, so the caller sees a real /tmp/... path
		//      and knows the capture did not land in the project tree. The
		//      caller is not blind.
		// This is a DOCUMENTED exemption, not an undocumented debug.Log.
		debug.Log("proxy", "audit dir unavailable, saving screenshot to temp dir: %v", err)
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
	ext := ".png"
	if strings.HasPrefix(dataURL, "data:image/jpeg") {
		ext = ".jpg"
	}
	filename := fmt.Sprintf("screenshot-%s-%s%s", sanitizeFilename(ps.ID), safeName, ext)
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
	auditDir, err := GetAuditDir(ps.Path)
	if err != nil {
		// DELIBERATE exemption from the Silent Failure Prohibition — see the
		// matching fallback in saveScreenshot above for the full rationale.
		// Summary: data-preservation beats loud-fail here (losing a capture is
		// worse than an odd location); a missing project root is legitimate for
		// some proxy shapes so surfacing every fallback would spam the agent
		// queue; and the returned filePath is the real /tmp/... location, so the
		// caller is not blind. Documented exemption, not an undocumented log.
		debug.Log("proxy", "audit dir unavailable, saving PNG to temp dir: %v", err)
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
	filename := fmt.Sprintf("agnt-result-%s-%s.json", sanitizeFilename(ps.ID), sanitizeFilename(execID))
	filePath := filepath.Join(tempDir, filename)

	err := os.WriteFile(filePath, []byte(result), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write large result: %w", err)
	}

	return filePath, nil
}

// SetActiveFrame records the content frame the page reported as active. Empty
// frameID is ignored (a report must name a frame).
func (ps *ProxyServer) SetActiveFrame(frameID string) {
	if frameID == "" {
		return
	}
	ps.activeFrameID.Store(&frameID)
}

// ActiveFrame returns the last content frame reported active, or "" if none.
func (ps *ProxyServer) ActiveFrame() string {
	if p := ps.activeFrameID.Load(); p != nil {
		return *p
	}
	return ""
}

// ExecTargetKind classifies a caller-supplied exec target selector. It is the
// single source of truth for the target-token vocabulary shared by the MCP tool
// layer (which builds the wire arg and rejects nonsense) and the proxy server
// (which resolves that arg to a concrete frame).
type ExecTargetKind int

const (
	// ExecTargetUnknown is a token that is neither a named selector nor a
	// server-minted frame id (a typo like "page"). The tool layer rejects it.
	ExecTargetUnknown ExecTargetKind = iota
	// ExecTargetChrome addresses the outer chrome shell (wire token "@chrome").
	ExecTargetChrome
	// ExecTargetActiveFrame lets the server pick the active content frame.
	ExecTargetActiveFrame
	// ExecTargetFrameID is an explicit server-minted frame id, passed through.
	ExecTargetFrameID
)

// ClassifyExecTarget maps a caller-supplied exec target selector onto its kind.
// Named selectors are case-insensitive and trimmed; anything else is a frame id
// only if it matches the server-minted frame-id shape, otherwise it is unknown.
func ClassifyExecTarget(target string) ExecTargetKind {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "@chrome", "outer", "shell", "chrome":
		return ExecTargetChrome
	case "inner", "active", "content", "":
		return ExecTargetActiveFrame
	default:
		if frameIDPattern.MatchString(target) {
			return ExecTargetFrameID
		}
		return ExecTargetUnknown
	}
}

// resolveExecTarget maps a caller-supplied frame selector onto the wire token
// the browser exec gate understands. The outer chrome shell is addressed by the
// "@chrome" role token (so the caller need not know the per-page shell id);
// "inner"/"active"/"" collapse to the active content frame; an explicit frame id
// (or an unknown token the tool layer already lets through) passes unchanged.
func (ps *ProxyServer) resolveExecTarget(target string) string {
	switch ClassifyExecTarget(target) {
	case ExecTargetChrome:
		return "@chrome"
	case ExecTargetActiveFrame:
		return ps.ActiveFrame()
	default:
		return target
	}
}

// DescribeExecTarget renders the frame an exec will actually run in, for
// diagnostics. It mirrors resolveExecTarget but returns a human-legible label
// so a timeout message reports the resolved destination rather than the raw
// caller arg (which is often "" and reads as "no frame" even though the exec
// was routed to the active content frame or broadcast to every frame).
func (ps *ProxyServer) DescribeExecTarget(target string) string {
	switch resolved := ps.resolveExecTarget(target); resolved {
	case "@chrome":
		return "@chrome (outer shell)"
	case "":
		return `<all frames> (no active content frame reported yet)`
	default:
		return resolved
	}
}

// ExecuteJavaScript sends JavaScript code to connected clients for execution.
// Returns the execution ID and a channel that will receive the result. An
// optional frameID targets a specific content frame; when empty it defaults to
// the active content frame (always-wrap model — docs/responsive-canonical-
// target.md §5.3). When both are empty the exec is untargeted and every
// connected (content) frame runs it (legacy behaviour).
func (ps *ProxyServer) ExecuteJavaScript(code string, frameID ...string) (string, <-chan *ExecutionResult, error) {
	target := ps.resolveExecTarget(firstFrameID(frameID))
	debug.Log("proxy", "ExecuteJavaScript: proxy=%s code_len=%d frame=%q", ps.ID, len(code), target)
	execID := fmt.Sprintf("exec-%d", time.Now().UnixNano())

	// Create result channel for this execution
	resultChan := make(chan *ExecutionResult, 1)
	ps.pendingExecs.Store(execID, resultChan)

	message := map[string]interface{}{
		"type":     "execute",
		"id":       execID,
		"code":     code,
		"frame_id": target,
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		ps.pendingExecs.Delete(execID)
		close(resultChan)
		return "", nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	// Send to all connected clients
	sentCount := ps.broadcastRaw(messageBytes)

	if sentCount == 0 {
		debug.Log("proxy", "ExecuteJavaScript: no connected clients for proxy %s", ps.ID)
		ps.pendingExecs.Delete(execID)
		close(resultChan)
		return execID, nil, fmt.Errorf("no connected clients")
	}

	// Self-expire the pending entry: if the browser never replies (page navigated
	// or reloaded mid-exec, or the caller dropped the channel without cancelling),
	// the pendingExecs entry and its result channel would leak forever. Reap them
	// after a bounded timeout. CancelExecution is a no-op when the result already
	// arrived (LoadAndDelete is atomic — exactly one of deliver/cancel wins).
	time.AfterFunc(execResultTimeout, func() { ps.CancelExecution(execID) })

	return execID, resultChan, nil
}

// CancelExecution drops a pending execution whose caller has given up waiting
// (e.g. a hub-side timeout). Without it the pendingExecs entry and its result
// channel leak forever when the browser never replies — common when the target
// page navigates or reloads mid-exec. Safe against the ws_handler delivery race:
// LoadAndDelete is atomic, so exactly one of cancel/deliver wins.
func (ps *ProxyServer) CancelExecution(execID string) {
	if ch, ok := ps.pendingExecs.LoadAndDelete(execID); ok {
		resultChan := ch.(chan *ExecutionResult)
		close(resultChan)
	}
}
