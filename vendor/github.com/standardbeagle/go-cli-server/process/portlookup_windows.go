//go:build windows

package process

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// findPIDsByPort uses "netstat -ano" to find PIDs listening on the given port.
// On Windows, PID 4 (System/HTTP.sys) may appear for .NET HttpListener ports.
// In that case, we resolve the actual application process via netsh or tasklist.
func findPIDsByPort(port int) []int {
	cmd := exec.Command("netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var pids []int
	seen := make(map[int]struct{})
	hasSystemPID := false
	portSuffix := fmt.Sprintf(":%d", port)

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP") || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if !strings.HasSuffix(fields[1], portSuffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
			continue
		}
		if pid == 4 {
			// PID 4 is the System process (HTTP.sys kernel driver).
			// .NET HttpListener registers URLs with HTTP.sys, so the
			// kernel holds the port. We can't kill PID 4 — instead find
			// the dotnet/application process that registered with it.
			hasSystemPID = true
			continue
		}
		if _, ok := seen[pid]; !ok {
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}

	// If PID 4 was the only listener, find the .NET process that registered it
	if hasSystemPID && len(pids) == 0 {
		pids = findHTTPSysOwners(port)
	}

	return pids
}

// findHTTPSysOwners finds application processes that registered with HTTP.sys
// for the given port. Uses "netsh http show servicestate" to find the request
// queue PID, falling back to searching for dotnet processes.
func findHTTPSysOwners(port int) []int {
	// Try netsh to find the registered controller process
	portStr := fmt.Sprintf(":%d", port)
	cmd := exec.Command("netsh", "http", "show", "servicestate", "view=requestq")
	output, err := cmd.Output()
	if err == nil {
		// Parse output looking for our port in registered URLs
		// then extract the controller process ID
		lines := strings.Split(string(output), "\n")
		for i, line := range lines {
			if !strings.Contains(line, portStr) {
				continue
			}
			// Look backwards for "Controller process ID:" line
			for j := i - 1; j >= 0 && j >= i-10; j-- {
				if strings.Contains(lines[j], "Controller process ID:") {
					parts := strings.Split(lines[j], ":")
					if len(parts) >= 2 {
						if pid, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1])); err == nil && pid > 4 {
							return []int{pid}
						}
					}
				}
			}
		}
	}

	// Fallback: find all dotnet processes (they're the most likely HTTP.sys users)
	cmd = exec.Command("tasklist", "/FI", "IMAGENAME eq dotnet.exe", "/FO", "CSV", "/NH")
	output, err = cmd.Output()
	if err != nil {
		return nil
	}

	var pids []int
	for _, line := range strings.Split(string(output), "\n") {
		// CSV format: "dotnet.exe","1234","Console","1","50,000 K"
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		pidStr := strings.Trim(fields[1], "\" ")
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
