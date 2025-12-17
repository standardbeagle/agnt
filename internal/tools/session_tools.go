package tools

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionInput defines input for the session tool.
type SessionInput struct {
	Action   string `json:"action" jsonschema:"Action: list, send, schedule, tasks, cancel, get"`
	Code     string `json:"code,omitempty" jsonschema:"Session code (required for send, schedule, get)"`
	Message  string `json:"message,omitempty" jsonschema:"Message to send or schedule (required for send, schedule)"`
	Duration string `json:"duration,omitempty" jsonschema:"Duration for scheduling (e.g. '5m', '1h30m') (required for schedule)"`
	TaskID   string `json:"task_id,omitempty" jsonschema:"Task ID (required for cancel)"`
	Global   bool   `json:"global,omitempty" jsonschema:"For list/tasks: include sessions/tasks from all directories (default: false)"`
}

// SessionOutput defines output for the session tool.
type SessionOutput struct {
	// For list
	Sessions []SessionEntry `json:"sessions,omitempty"`
	Count    int            `json:"count,omitempty"`

	// For get
	Session *SessionEntry `json:"session,omitempty"`

	// For tasks
	Tasks []TaskEntry `json:"tasks,omitempty"`

	// For send/schedule
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
	TaskID  string `json:"task_id,omitempty"`

	// For schedule
	DeliverAt *time.Time `json:"deliver_at,omitempty"`

	// Directory filtering info
	Directory string `json:"directory,omitempty"`
	Global    bool   `json:"global,omitempty"`
}

// SessionEntry represents a session in the list.
type SessionEntry struct {
	Code        string    `json:"code"`
	OverlayPath string    `json:"overlay_path,omitempty"`
	ProjectPath string    `json:"project_path,omitempty"`
	Command     string    `json:"command,omitempty"`
	Args        []string  `json:"args,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	Status      string    `json:"status,omitempty"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
}

// TaskEntry represents a scheduled task in the list.
type TaskEntry struct {
	ID          string    `json:"id"`
	SessionCode string    `json:"session_code"`
	Message     string    `json:"message"`
	DeliverAt   time.Time `json:"deliver_at"`
	CreatedAt   time.Time `json:"created_at"`
	ProjectPath string    `json:"project_path,omitempty"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// RegisterSessionTool adds the session MCP tool to the server.
func RegisterSessionTool(server *mcp.Server, dt *DaemonTools) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "session",
		Description: `Manage agnt run sessions and schedule messages for AI agents.

Sessions are created automatically when 'agnt run' starts. You can send messages
directly to a session or schedule messages for future delivery.

Actions:
  list: List active sessions (filtered by current directory unless global: true)
  get: Get details for a specific session
  send: Send a message to a session immediately
  schedule: Schedule a message for future delivery
  tasks: List scheduled tasks
  cancel: Cancel a scheduled task

Examples:
  session {action: "list"}
  session {action: "list", global: true}
  session {action: "get", code: "claude-1"}
  session {action: "send", code: "claude-1", message: "Check the test results"}
  session {action: "schedule", code: "claude-1", duration: "5m", message: "Verify this completed"}
  session {action: "tasks"}
  session {action: "cancel", task_id: "task-abc123"}

Duration format:
  - "5m" = 5 minutes
  - "1h" = 1 hour
  - "1h30m" = 1 hour 30 minutes
  - "30s" = 30 seconds

Scheduled messages are delivered as synthetic stdin to the AI agent's PTY,
allowing you to remind the agent to check on tasks or verify completions.`,
	}, dt.makeSessionHandler())
}

// makeSessionHandler creates a handler for the session tool.
func (dt *DaemonTools) makeSessionHandler() func(context.Context, *mcp.CallToolRequest, SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), SessionOutput{}, nil
		}

		switch input.Action {
		case "list":
			return dt.handleSessionList(input)
		case "get":
			return dt.handleSessionGet(input)
		case "send":
			return dt.handleSessionSend(input)
		case "schedule":
			return dt.handleSessionSchedule(input)
		case "tasks":
			return dt.handleSessionTasks(input)
		case "cancel":
			return dt.handleSessionCancel(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: list, get, send, schedule, tasks, cancel", input.Action)), SessionOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleSessionList(input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get working directory: %v", err)), SessionOutput{}, nil
	}

	// Create directory filter
	dirFilter := protocol.DirectoryFilter{
		Directory: cwd,
		Global:    input.Global,
	}

	result, err := dt.client.SessionList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "session"), SessionOutput{}, nil
	}

	output := SessionOutput{
		Count:     getInt(result, "count"),
		Directory: getString(result, "directory"),
		Global:    getBool(result, "global"),
	}

	if sessions, ok := result["sessions"].([]interface{}); ok {
		for _, s := range sessions {
			if sm, ok := s.(map[string]interface{}); ok {
				entry := SessionEntry{
					Code:        getString(sm, "code"),
					OverlayPath: getString(sm, "overlay_path"),
					ProjectPath: getString(sm, "project_path"),
					Command:     getString(sm, "command"),
					Status:      getString(sm, "status"),
				}
				if args, ok := sm["args"].([]interface{}); ok {
					for _, a := range args {
						if str, ok := a.(string); ok {
							entry.Args = append(entry.Args, str)
						}
					}
				}
				if ts, ok := sm["started_at"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.StartedAt = t
					}
				}
				if ts, ok := sm["last_seen"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.LastSeen = t
					}
				}
				output.Sessions = append(output.Sessions, entry)
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleSessionGet(input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	if input.Code == "" {
		return errorResult("code required for get"), SessionOutput{}, nil
	}

	result, err := dt.client.SessionGet(input.Code)
	if err != nil {
		return formatDaemonError(err, "session"), SessionOutput{}, nil
	}

	entry := SessionEntry{
		Code:        getString(result, "code"),
		OverlayPath: getString(result, "overlay_path"),
		ProjectPath: getString(result, "project_path"),
		Command:     getString(result, "command"),
		Status:      getString(result, "status"),
	}
	if args, ok := result["args"].([]interface{}); ok {
		for _, a := range args {
			if str, ok := a.(string); ok {
				entry.Args = append(entry.Args, str)
			}
		}
	}
	if ts, ok := result["started_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			entry.StartedAt = t
		}
	}
	if ts, ok := result["last_seen"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			entry.LastSeen = t
		}
	}

	return nil, SessionOutput{Session: &entry}, nil
}

func (dt *DaemonTools) handleSessionSend(input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	if input.Code == "" {
		return errorResult("code required for send"), SessionOutput{}, nil
	}
	if input.Message == "" {
		return errorResult("message required for send"), SessionOutput{}, nil
	}

	result, err := dt.client.SessionSend(input.Code, input.Message)
	if err != nil {
		return formatDaemonError(err, "session"), SessionOutput{}, nil
	}

	return nil, SessionOutput{
		Success: getBool(result, "success"),
		Message: getString(result, "message"),
	}, nil
}

func (dt *DaemonTools) handleSessionSchedule(input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	if input.Code == "" {
		return errorResult("code required for schedule"), SessionOutput{}, nil
	}
	if input.Duration == "" {
		return errorResult("duration required for schedule (e.g. '5m', '1h30m')"), SessionOutput{}, nil
	}
	if input.Message == "" {
		return errorResult("message required for schedule"), SessionOutput{}, nil
	}

	result, err := dt.client.SessionSchedule(input.Code, input.Duration, input.Message)
	if err != nil {
		return formatDaemonError(err, "session"), SessionOutput{}, nil
	}

	output := SessionOutput{
		Success: getBool(result, "success"),
		Message: getString(result, "message"),
		TaskID:  getString(result, "task_id"),
	}

	if ts, ok := result["deliver_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			output.DeliverAt = &t
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleSessionTasks(input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get working directory: %v", err)), SessionOutput{}, nil
	}

	// Create directory filter
	dirFilter := protocol.DirectoryFilter{
		Directory: cwd,
		Global:    input.Global,
	}

	result, err := dt.client.SessionTasks(dirFilter)
	if err != nil {
		return formatDaemonError(err, "session"), SessionOutput{}, nil
	}

	output := SessionOutput{
		Count:     getInt(result, "count"),
		Directory: getString(result, "directory"),
		Global:    getBool(result, "global"),
	}

	if tasks, ok := result["tasks"].([]interface{}); ok {
		for _, t := range tasks {
			if tm, ok := t.(map[string]interface{}); ok {
				entry := TaskEntry{
					ID:          getString(tm, "id"),
					SessionCode: getString(tm, "session_code"),
					Message:     getString(tm, "message"),
					ProjectPath: getString(tm, "project_path"),
					Status:      getString(tm, "status"),
					Attempts:    getInt(tm, "attempts"),
					LastError:   getString(tm, "last_error"),
				}
				if ts, ok := tm["deliver_at"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.DeliverAt = t
					}
				}
				if ts, ok := tm["created_at"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.CreatedAt = t
					}
				}
				output.Tasks = append(output.Tasks, entry)
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleSessionCancel(input SessionInput) (*mcp.CallToolResult, SessionOutput, error) {
	if input.TaskID == "" {
		return errorResult("task_id required for cancel"), SessionOutput{}, nil
	}

	err := dt.client.SessionCancel(input.TaskID)
	if err != nil {
		return formatDaemonError(err, "session"), SessionOutput{}, nil
	}

	return nil, SessionOutput{
		Success: true,
		Message: fmt.Sprintf("Task %s cancelled", input.TaskID),
	}, nil
}
