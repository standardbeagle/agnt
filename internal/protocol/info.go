package protocol

import (
	"time"

	"github.com/standardbeagle/agnt/internal/updater"
)

// DaemonInfo holds daemon status information. It is the wire DTO returned by
// the INFO verb: the daemon constructs it, the client unmarshals it.
type DaemonInfo struct {
	Version       string              `json:"version"`
	BuildTime     string              `json:"build_time,omitempty"`
	GitCommit     string              `json:"git_commit,omitempty"`
	SocketPath    string              `json:"socket_path"`
	Uptime        time.Duration       `json:"uptime"`
	ClientCount   int64               `json:"client_count"`
	ProcessInfo   ProcessInfo         `json:"process_info"`
	ProxyInfo     ProxyInfo           `json:"proxy_info"`
	TunnelInfo    TunnelInfo          `json:"tunnel_info"`
	BrowserInfo   BrowserInfo         `json:"browser_info"`
	SessionInfo   SessionInfo         `json:"session_info"`
	SchedulerInfo SchedulerInfo       `json:"scheduler_info"`
	UpdateInfo    *updater.UpdateInfo `json:"update_info,omitempty"`
}

// ProcessInfo holds process manager statistics.
type ProcessInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
	TotalFailed  int64 `json:"total_failed"`
}

// ProxyInfo holds proxy manager statistics.
type ProxyInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
}

// TunnelInfo holds tunnel manager statistics.
type TunnelInfo struct {
	Active int64 `json:"active"`
}

// BrowserInfo holds browser manager statistics.
type BrowserInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
}

// SessionInfo contains statistics about the session registry.
type SessionInfo struct {
	ActiveCount       int64 `json:"active_count"`
	TotalRegistered   int64 `json:"total_registered"`
	TotalUnregistered int64 `json:"total_unregistered"`
}

// SchedulerInfo contains statistics about the scheduler.
type SchedulerInfo struct {
	TotalScheduled int64 `json:"total_scheduled"`
	TotalDelivered int64 `json:"total_delivered"`
	TotalFailed    int64 `json:"total_failed"`
	TotalCancelled int64 `json:"total_cancelled"`
	PendingCount   int64 `json:"pending_count"`
}
