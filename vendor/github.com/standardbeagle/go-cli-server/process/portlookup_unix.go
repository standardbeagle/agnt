//go:build !windows

package process

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func findPIDsByPort(port int) []int {
	if runtime.GOOS == "linux" {
		return findPIDsByPortProc(port)
	}
	// macOS: fall back to lsof
	return findPIDsByPortLsof(port)
}

// findPIDsByPortProc parses /proc/net/tcp and /proc/net/tcp6 to find inodes
// for sockets listening on the given port, then scans /proc/*/fd/ to map
// those inodes to PIDs.
func findPIDsByPortProc(port int) []int {
	inodes := findListeningInodes(port)
	if len(inodes) == 0 {
		return nil
	}
	return findPIDsForInodes(inodes)
}

// findListeningInodes reads /proc/net/tcp and /proc/net/tcp6 for sockets
// in LISTEN state (0A) on the target port, returning their inode numbers.
func findListeningInodes(port int) map[string]struct{} {
	inodes := make(map[string]struct{})
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
			// fields[1] = local_address (hex_ip:hex_port)
			// fields[3] = state (0A = LISTEN)
			if fields[3] != "0A" {
				continue
			}
			localAddr := fields[1]
			idx := strings.LastIndex(localAddr, ":")
			if idx == -1 {
				continue
			}
			portHex := localAddr[idx+1:]
			portBytes, err := hex.DecodeString(portHex)
			if err != nil || len(portBytes) != 2 {
				continue
			}
			listenPort := int(portBytes[0])<<8 | int(portBytes[1])
			if listenPort == port {
				inodes[fields[9]] = struct{}{}
			}
		}
	}
	return inodes
}

// findPIDsForInodes scans /proc/*/fd/ to find which PIDs hold the given
// socket inodes.
func findPIDsForInodes(inodes map[string]struct{}) []int {
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
			// link format: "socket:[inode]"
			if !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := link[8 : len(link)-1]
			if _, ok := inodes[inode]; ok {
				pids = append(pids, pid)
				break // one match per PID is enough
			}
		}
	}
	return pids
}

// findPIDsByPortLsof uses lsof to find PIDs listening on the given port.
// Used on macOS where /proc is not available.
func findPIDsByPortLsof(port int) []int {
	cmd := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port))
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parsePIDOutput(strings.TrimSpace(string(output)))
}
