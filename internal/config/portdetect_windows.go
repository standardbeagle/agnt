//go:build windows

package config

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

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

// ProcessNameByPID returns the process name for a given PID.
// TODO: implement via Windows API (CreateToolhelp32Snapshot)
func ProcessNameByPID(pid int) string {
	return ""
}
