//go:build windows

package config

import (
	"context"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// PortOwner is a single listening TCP port and the process that owns it.
type PortOwner struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	Windows bool   `json:"windows"`
}

// netstatOutput runs `netstat -ano`. A variable so tests can feed localized
// output without depending on the host's display language.
var netstatOutput = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "netstat", "-ano").Output()
}

// ListListeningPorts enumerates every listening TCP port via netstat -ano,
// deduped per port across IPv4/IPv6 rows. Listener rows are recognised by
// shape, not by the state word, which netstat localizes (ABHÖREN on German
// Windows). On native Windows every owner is a Windows process, so Windows is
// true throughout. Returns ports sorted ascending.
func ListListeningPorts(ctx context.Context) []PortOwner {
	output, err := netstatOutput(ctx)
	if err != nil {
		return nil
	}
	out := parseNetstatExeAllListeners(output)
	sortPortOwners(out)
	return out
}

// ProcessNameByPID returns the executable name (e.g. "node.exe") of the process
// with the given PID, or "" when the PID is invalid, gone, or not queryable —
// a protected system process refuses even the limited-information handle.
func ProcessNameByPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:size]))
}

// ProcessNamesByPIDs returns a map of PID → process name for all given PIDs.
// PIDs whose name cannot be resolved are omitted.
func ProcessNamesByPIDs(ctx context.Context, pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	for _, pid := range pids {
		if name := ProcessNameByPID(pid); name != "" {
			out[pid] = name
		}
	}
	return out
}

// FindPIDsByPortTagged is the kill-routing-aware variant of FindPIDsByPort.
// On native Windows there is no Linux/Windows split — all PIDs are
// Windows-side by definition — so the linux slice is always nil and every
// PID is returned in the windows slice.
func FindPIDsByPortTagged(ctx context.Context, port int) (linuxPIDs, windowsPIDs []int) {
	return nil, FindPIDsByPort(ctx, port)
}

// FindPIDsByPort returns PIDs of processes listening on the given TCP port.
// Uses netstat -ano on Windows; see parseNetstatExePIDs for the row rules.
func FindPIDsByPort(ctx context.Context, port int) []int {
	output, err := netstatOutput(ctx)
	if err != nil {
		return nil
	}
	return parseNetstatExePIDs(output, port)
}
