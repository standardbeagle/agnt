package proxy

import "github.com/standardbeagle/agnt/internal/protocol"

// SessionClient defines the interface for session operations.
// This interface is implemented by daemon.Client to avoid import cycles.
type SessionClient interface {
	SessionList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error)
	SessionGet(code string) (map[string]interface{}, error)
	SessionSend(code string, message string) (map[string]interface{}, error)
	SessionSchedule(code string, duration string, message string) (map[string]interface{}, error)
	SessionTasks(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error)
	SessionCancel(taskID string) error
	Close() error
}

// SessionClientFactory is a function that creates a new SessionClient.
// This is used to avoid import cycles by having the daemon package provide the factory.
type SessionClientFactory func() (SessionClient, error)
