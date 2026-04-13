package process

import (
	"strconv"
	"strings"
)

// FindPIDsByPort returns PIDs of processes listening on the given TCP port.
// It uses platform-native APIs: /proc/net/tcp on Linux, lsof on macOS,
// and netstat on Windows. Safe for concurrent use.
func FindPIDsByPort(port int) []int {
	return findPIDsByPort(port)
}

// parsePIDOutput parses newline-separated PID strings into ints.
func parsePIDOutput(output string) []int {
	if output == "" {
		return nil
	}
	lines := strings.Split(output, "\n")
	var pids []int
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
