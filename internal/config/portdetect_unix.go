//go:build !windows

package config

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// detectPortsForPID finds listening TCP ports for a given PID using
// /proc/net/tcp on Linux or lsof on macOS.
func detectPortsForPID(ctx context.Context, pid int) []int {
	if runtime.GOOS == "linux" {
		return detectPortsForPIDProc(pid)
	}
	return detectPortsForPIDLsof(ctx, pid)
}

// detectPortsForPIDProc reads /proc/net/tcp{,6} to find listening sockets,
// then checks /proc/<pid>/fd/ to see which belong to the target PID.
func detectPortsForPIDProc(pid int) []int {
	// Collect all listening sockets: inode -> port
	inodePorts := make(map[string]int)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// fields[3] = state (0A = LISTEN)
			if fields[3] != "0A" {
				continue
			}
			localAddr := fields[1]
			idx := strings.LastIndex(localAddr, ":")
			if idx == -1 {
				continue
			}
			portBytes, err := hex.DecodeString(localAddr[idx+1:])
			if err != nil || len(portBytes) != 2 {
				continue
			}
			port := int(portBytes[0])<<8 | int(portBytes[1])
			if port > 0 && port < 65536 {
				inodePorts[fields[9]] = port
			}
		}
	}
	if len(inodePorts) == 0 {
		return nil
	}

	// Check which inodes belong to our PID
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}

	seen := make(map[int]struct{})
	var ports []int
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(link, "socket:[") {
			continue
		}
		inode := link[8 : len(link)-1]
		if port, ok := inodePorts[inode]; ok {
			if _, dup := seen[port]; !dup {
				seen[port] = struct{}{}
				ports = append(ports, port)
			}
		}
	}
	return ports
}

// detectPortsForPIDLsof uses lsof to find listening ports for a process.
// Used on macOS where /proc is not available.
func detectPortsForPIDLsof(ctx context.Context, pid int) []int {
	cmd := exec.CommandContext(ctx, "lsof", "-iTCP", "-sTCP:LISTEN", "-p", strconv.Itoa(pid), "-n", "-P")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var ports []int
	portRe := regexp.MustCompile(`:(\d+)\s+\(LISTEN\)`)
	for _, line := range strings.Split(string(output), "\n") {
		if matches := portRe.FindStringSubmatch(line); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
				ports = append(ports, port)
			}
		}
	}
	return ports
}

// FindPIDsByPort returns PIDs of processes listening on the given TCP port.
// Uses /proc/net/tcp on Linux or lsof on macOS.
func FindPIDsByPort(ctx context.Context, port int) []int {
	if runtime.GOOS == "linux" {
		return findPIDsByPortProc(port)
	}
	return findPIDsByPortLsof(ctx, port)
}

// findPIDsByPortProc parses /proc/net/tcp{,6} for sockets listening on the
// target port, then scans /proc/*/fd/ to map inodes to PIDs.
func findPIDsByPortProc(port int) []int {
	// Find inodes for sockets listening on target port
	inodes := make(map[string]struct{})
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			localAddr := fields[1]
			idx := strings.LastIndex(localAddr, ":")
			if idx == -1 {
				continue
			}
			portBytes, err := hex.DecodeString(localAddr[idx+1:])
			if err != nil || len(portBytes) != 2 {
				continue
			}
			listenPort := int(portBytes[0])<<8 | int(portBytes[1])
			if listenPort == port {
				inodes[fields[9]] = struct{}{}
			}
		}
	}
	if len(inodes) == 0 {
		return nil
	}

	// Scan /proc/*/fd/ for matching inodes
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := link[8 : len(link)-1]
			if _, ok := inodes[inode]; ok {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// ProcessNameByPID returns the process name for a given PID.
// Returns empty string if the PID doesn't exist or can't be read.
func ProcessNameByPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err == nil {
			name := strings.TrimSpace(string(data))
			if name != "" {
				return name
			}
		}
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(data), "\x00", 2)
	if len(parts) > 0 && parts[0] != "" {
		return filepath.Base(parts[0])
	}
	return ""
}

// findPIDsByPortLsof uses lsof to find PIDs on macOS.
func findPIDsByPortLsof(ctx context.Context, port int) []int {
	cmd := exec.CommandContext(ctx, "lsof", "-ti", fmt.Sprintf(":%d", port))
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
