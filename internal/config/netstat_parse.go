package config

// netstat -ano parsing shared by native Windows (portdetect_windows.go) and the
// WSL netstat.exe fallback (portdetect_unix.go). Built on every platform so the
// parser's tests run everywhere.

import (
	"sort"
	"strconv"
	"strings"
)

// parseNetstatExePIDs is the pure parser behind native-Windows FindPIDsByPort
// and the WSL fallback findPIDsByPortNetstatExe.
// It extracts the PIDs of LISTENING TCP sockets bound to the target port
// from `netstat.exe -ano` output. Typical rows:
//
//	TCP    0.0.0.0:80     0.0.0.0:0    LISTENING    1234
//	TCP    [::]:80        [::]:0       LISTENING    1234
//
// Rules:
//   - Only the stable five-column TCP shape is considered. Listener rows have
//     remote endpoint port 0; this avoids depending on the localized state
//     word (LISTENING, ABHÖREN, etc.).
//   - The local-address port is parsed numerically after its final colon,
//     guarding IPv4/IPv6 and :80-vs-:8080 false positives.
//   - The PID is the last field (fields[4]); rows with <5 fields or a
//     non-numeric/zero PID are skipped.
//   - PIDs are de-duplicated (a process may hold both the IPv4 and IPv6
//     wildcard socket for the same port, producing two rows).
//
// Returns nil when no row matches.
func parseNetstatExePIDs(output []byte, port int) []int {
	var pids []int
	seen := make(map[int]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		localPort, pid, ok := parseNetstatExeListenerRow(line)
		if !ok || localPort != port {
			continue
		}
		if _, dup := seen[pid]; !dup {
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}
	return pids
}

func parseNetstatExeListenerRow(line string) (port, pid int, ok bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 5 || !strings.EqualFold(fields[0], "TCP") {
		return 0, 0, false
	}
	localPort, ok := netstatEndpointPort(fields[1])
	if !ok || localPort <= 0 {
		return 0, 0, false
	}
	remotePort, ok := netstatEndpointPort(fields[2])
	if !ok || remotePort != 0 {
		return 0, 0, false
	}
	ownerPID, err := strconv.Atoi(fields[4])
	if err != nil || ownerPID <= 0 {
		return 0, 0, false
	}
	return localPort, ownerPID, true
}

func netstatEndpointPort(endpoint string) (int, bool) {
	idx := strings.LastIndexByte(endpoint, ':')
	if idx < 0 || idx == len(endpoint)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(endpoint[idx+1:])
	return port, err == nil && port >= 0 && port <= 65535
}

// parseNetstatExeAllListeners parses `netstat.exe -ano` output into one
// PortOwner per LISTENING TCP port (deduped across the IPv4/IPv6 rows a single
// process holds). Malformed rows are dropped.
func parseNetstatExeAllListeners(output []byte) []PortOwner {
	var out []PortOwner
	seen := make(map[int]bool)
	for _, line := range strings.Split(string(output), "\n") {
		port, pid, ok := parseNetstatExeListenerRow(line)
		if !ok {
			continue
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, PortOwner{Port: port, PID: pid, Windows: true})
	}
	return out
}

// sortPortOwners orders owners by port ascending.
func sortPortOwners(owners []PortOwner) {
	sort.Slice(owners, func(i, j int) bool { return owners[i].Port < owners[j].Port })
}
