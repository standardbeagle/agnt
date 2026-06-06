//go:build windows

package config

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// PortOwner is a single listening TCP port and the process that owns it.
type PortOwner struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	Windows bool   `json:"windows"`
}

// ListListeningPorts enumerates every LISTENING TCP port via netstat -ano,
// deduped per port across IPv4/IPv6 rows. On native Windows every owner is a
// Windows process, so Windows is true throughout. Returns ports sorted ascending.
func ListListeningPorts(ctx context.Context) []PortOwner {
	output, err := exec.CommandContext(ctx, "netstat", "-ano").Output()
	if err != nil {
		return nil
	}
	seen := make(map[int]bool)
	var out []PortOwner
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP") || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		idx := strings.LastIndex(fields[1], ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(fields[1][idx+1:])
		if err != nil || port <= 0 || port > 65535 || seen[port] {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
			continue
		}
		seen[port] = true
		out = append(out, PortOwner{Port: port, PID: pid, Windows: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// detectPortsForPID finds listening TCP ports for a given PID using netstat.
func detectPortsForPID(ctx context.Context, pid int) []int {
	cmd := exec.CommandContext(ctx, "netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	pidStr := strconv.Itoa(pid)
	seen := make(map[int]struct{})
	var ports []int

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP") || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[4] != pidStr {
			continue
		}
		localAddr := fields[1]
		idx := strings.LastIndex(localAddr, ":")
		if idx == -1 {
			continue
		}
		port, err := strconv.Atoi(localAddr[idx+1:])
		if err != nil || port <= 0 || port >= 65536 {
			continue
		}
		if _, dup := seen[port]; !dup {
			seen[port] = struct{}{}
			ports = append(ports, port)
		}
	}
	return ports
}

// ProcessNameByPID returns the process name for a given PID.
func ProcessNameByPID(pid int) string {
	return ""
}

// ProcessNamesByPIDs returns a map of PID → process name for all given PIDs.
// On Windows, ProcessNameByPID always returns "" (not yet implemented).
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
// Uses netstat -ano on Windows.
func FindPIDsByPort(ctx context.Context, port int) []int {
	cmd := exec.CommandContext(ctx, "netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var pids []int
	seen := make(map[int]struct{})
	portSuffix := fmt.Sprintf(":%d", port)

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP") || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasSuffix(fields[1], portSuffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; !ok {
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}
	return pids
}
