//go:build windows

package process

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var portTokenPattern = regexp.MustCompile(`:\d+(?:[/?\s]|$)`)

// findPIDsByPort uses "netstat -ano" to find PIDs listening on the given port.
// On Windows, PID 4 (System/HTTP.sys) may appear for .NET HttpListener ports.
// In that case, we resolve the actual application process via netsh. If netsh
// cannot identify an exact owner, fail closed instead of killing by guess.
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
// queue PID and returns nil if the owner cannot be identified exactly.
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
			if !lineContainsExactPort(line, portStr) {
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

	return nil
}

func lineContainsExactPort(line, portStr string) bool {
	for _, token := range portTokenPattern.FindAllString(line, -1) {
		if strings.TrimRight(token, "/? \t\r\n") == portStr {
			return true
		}
	}
	return false
}
