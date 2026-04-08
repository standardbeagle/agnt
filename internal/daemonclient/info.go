package daemonclient

import (
	"time"

	"github.com/standardbeagle/agnt/internal/scheduler"
	"github.com/standardbeagle/agnt/internal/session"
	"github.com/standardbeagle/agnt/internal/updater"
)

// DaemonInfo holds daemon status information.
type DaemonInfo struct {
	Version       string                  `json:"version"`
	BuildTime     string                  `json:"build_time,omitempty"`
	GitCommit     string                  `json:"git_commit,omitempty"`
	SocketPath    string                  `json:"socket_path"`
	Uptime        time.Duration           `json:"uptime"`
	ClientCount   int64                   `json:"client_count"`
	ProcessInfo   ProcessInfo             `json:"process_info"`
	ProxyInfo     ProxyInfo               `json:"proxy_info"`
	TunnelInfo    TunnelInfo              `json:"tunnel_info"`
	BrowserInfo   BrowserInfo             `json:"browser_info"`
	SessionInfo   session.SessionInfo     `json:"session_info"`
	SchedulerInfo scheduler.SchedulerInfo `json:"scheduler_info"`
	UpdateInfo    *updater.UpdateInfo     `json:"update_info,omitempty"`
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
