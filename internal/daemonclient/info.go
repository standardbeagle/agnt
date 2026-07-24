package daemonclient

import (
	"github.com/standardbeagle/agnt/internal/protocol"
)

// The INFO wire DTOs live in internal/protocol — the daemon constructs them
// and must not import its own client package (layering inversion). These
// aliases keep existing client call sites compiling unchanged.
type (
	DaemonInfo    = protocol.DaemonInfo
	ProcessInfo   = protocol.ProcessInfo
	ProxyInfo     = protocol.ProxyInfo
	TunnelInfo    = protocol.TunnelInfo
	BrowserInfo   = protocol.BrowserInfo
	SessionInfo   = protocol.SessionInfo
	SchedulerInfo = protocol.SchedulerInfo
)
