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
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/standardbeagle/agnt/internal/platform"
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
			inode, port, ok := parseProcNetTCPLine(line)
			if !ok {
				continue
			}
			inodePorts[inode] = port
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

// parseProcNetTCPLine parses a single /proc/net/tcp{,6} data row (the header
// line must be stripped by the caller) and returns the listening socket's
// inode and local port. ok is false for any non-listening, malformed, or
// out-of-range row.
//
// The /proc/net/tcp columns are:
//
//		sl  local_address rem_address st ... inode
//		 0       1             2       3 ...   9
//
//	  - local_address is "HEXIP:HEXPORT" where HEXPORT is a big-endian 2-byte
//	    hex value (e.g. ":1F90" => 8080). IPv6 (/proc/net/tcp6) uses a longer
//	    hex IP but the same ":HEXPORT" suffix.
//	  - st (field 3) is the connection state; "0A" is TCP_LISTEN. Any other
//	    state (established, time-wait, etc.) is skipped.
//	  - inode is field 9; it is the key that /proc/<pid>/fd socket links
//	    resolve back to.
//
// Rows with fewer than 10 fields, a non-0A state, a missing ':' in the
// local address, an unparseable/oddly-sized hex port, or a port outside
// (0, 65536) are rejected with ok=false.
func parseProcNetTCPLine(line string) (inode string, port int, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return "", 0, false
	}
	// fields[3] = state (0A = LISTEN)
	if fields[3] != "0A" {
		return "", 0, false
	}
	localAddr := fields[1]
	idx := strings.LastIndex(localAddr, ":")
	if idx == -1 {
		return "", 0, false
	}
	portBytes, err := hex.DecodeString(localAddr[idx+1:])
	if err != nil || len(portBytes) != 2 {
		return "", 0, false
	}
	p := int(portBytes[0])<<8 | int(portBytes[1])
	if p <= 0 || p >= 65536 {
		return "", 0, false
	}
	return fields[9], p, true
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
//
// On WSL, /proc/net/tcp only sees Linux-side listeners. When the Linux scan
// returns zero PIDs and we are running under WSL, we fall back to
// netstat.exe -ano to surface Windows-side listeners (browsers, Docker
// Desktop, IIS, etc.) holding the port. The fallback is conditional, not
// additive, because netstat.exe shells out and parses CSV (~50-150 ms),
// which would dominate the hot path of port preflight / autostart cleanup
// on the 99% case where the owner is a Linux process.
//
// Callers that need to kill the returned PIDs should use FindPIDsByPortTagged
// instead — Windows-side PIDs require taskkill.exe (platform.KillWindowsPID),
// while Linux-side PIDs use syscall.Kill via the normal ProcessManager path.
// FindPIDsByPort remains the right call when you only need visibility (doctor,
// preflight detection, status display).
func FindPIDsByPort(ctx context.Context, port int) []int {
	linux, windows := FindPIDsByPortTagged(ctx, port)
	if len(linux) == 0 {
		return windows
	}
	if len(windows) == 0 {
		return linux
	}
	return append(linux, windows...)
}

// FindPIDsByPortTagged is the kill-routing-aware variant of FindPIDsByPort.
// Returns two slices: PIDs sourced from the Linux scan (/proc/net/tcp on
// Linux, lsof on macOS) and PIDs sourced from the Windows scan (netstat.exe
// on WSL when the Linux scan was empty). The split tells callers which kill
// path to use:
//
//   - Linux PIDs → syscall.Kill via ProcessManager.KillProcessByPort
//   - Windows PIDs → platform.KillWindowsPID (shells to taskkill.exe)
//
// On macOS and non-WSL Linux hosts the windows slice is always nil. On
// native Windows builds (portdetect_windows.go) all PIDs are tagged as
// Windows-side because there is no Linux namespace to be in. The fallback
// shape — Linux scan first, Windows scan only when Linux is empty —
// matches FindPIDsByPort to preserve hot-path latency: netstat.exe shells
// out (~50-150ms) and we don't pay it when /proc/net/tcp answers.
func FindPIDsByPortTagged(ctx context.Context, port int) (linuxPIDs, windowsPIDs []int) {
	if runtime.GOOS == "linux" {
		linux := findPIDsByPortProc(port)
		if len(linux) == 0 && platform.IsWSL() {
			return nil, findPIDsByPortNetstatExe(ctx, port)
		}
		return linux, nil
	}
	return findPIDsByPortLsof(ctx, port), nil
}

// findPIDsByPortNetstatExe shells to Windows-side netstat.exe (visible from
// WSL via interop) to find PIDs listening on a port. Returns nil on any
// failure — netstat.exe missing, malformed output, no match. Used as a
// best-effort fallback when /proc/net/tcp on WSL returned no Linux-side
// owner for a port we expected to be in use.
func findPIDsByPortNetstatExe(ctx context.Context, port int) []int {
	exe, err := exec.LookPath("netstat.exe")
	if err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, exe, "-ano")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseNetstatExePIDs(output, port)
}

// parseNetstatExePIDs is the pure parser behind findPIDsByPortNetstatExe.
// It extracts the PIDs of LISTENING TCP sockets bound to the target port
// from `netstat.exe -ano` output. Typical rows:
//
//	TCP    0.0.0.0:80     0.0.0.0:0    LISTENING    1234
//	TCP    [::]:80        [::]:0       LISTENING    1234
//
// Rules:
//   - Only rows whose first field is "TCP" and that contain "LISTENING"
//     are considered (UDP rows have no state column; ESTABLISHED/TIME_WAIT
//     rows are not bind owners we care about).
//   - The local-address field (fields[1]) must end in ":<port>" exactly.
//     The exact-suffix check guards the :80-vs-:8080 false positive — ":80"
//     is not a suffix of "0.0.0.0:8080".
//   - The PID is the last field (fields[4]); rows with <5 fields or a
//     non-numeric/zero PID are skipped.
//   - PIDs are de-duplicated (a process may hold both the IPv4 and IPv6
//     wildcard socket for the same port, producing two rows).
//
// Returns nil when no row matches.
func parseNetstatExePIDs(output []byte, port int) []int {
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
		if _, dup := seen[pid]; !dup {
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}
	return pids
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
			inode, listenPort, ok := parseProcNetTCPLine(line)
			if !ok {
				continue
			}
			if listenPort == port {
				inodes[inode] = struct{}{}
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
//
// On WSL, when /proc lookup misses (the PID is a Windows-side process
// surfaced by FindPIDsByPort's netstat.exe fallback), we shell out to
// tasklist.exe as a one-shot resolution. The tasklist.exe call is
// skipped entirely when /proc resolves, so purely-Linux PIDs pay zero
// fallback cost.
//
// For batch resolution of multiple rogue PIDs (e.g. doctor command),
// prefer ProcessNamesByPIDs which coalesces N PIDs into a single
// tasklist.exe invocation.
func ProcessNameByPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	if name := procNameLinux(pid); name != "" {
		return name
	}
	// /proc miss: on WSL, the PID may belong to a Windows-side process
	// reported by netstat.exe. tasklist.exe is the only way to resolve
	// the name. Conditional fallback — never paid by Linux PIDs.
	if platform.IsWSL() {
		names := tasklistResolve(context.Background(), []int{pid})
		if name, ok := names[pid]; ok {
			return name
		}
	}
	return ""
}

// procNameLinux returns the process name from /proc/<pid>/comm, falling
// back to /proc/<pid>/cmdline. Returns empty when the PID has no /proc
// entry (e.g. Windows-side PID seen from WSL).
func procNameLinux(pid int) string {
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

// ProcessNamesByPIDs resolves a batch of PIDs to process names. Linux
// PIDs are resolved via /proc; PIDs that miss /proc are batched into a
// single tasklist.exe invocation on WSL. Returns a map keyed by PID;
// PIDs that resolve to nothing (no /proc entry, no tasklist.exe match,
// or running outside WSL) are simply absent from the map.
//
// The batched tasklist.exe call is the cache discipline called for in
// the WSL-doctor task: N rogue PIDs cost at most one shell-out, not N.
func ProcessNamesByPIDs(ctx context.Context, pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	if len(pids) == 0 {
		return out
	}

	var procMisses []int
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if name := procNameLinux(pid); name != "" {
			out[pid] = name
			continue
		}
		procMisses = append(procMisses, pid)
	}

	if len(procMisses) == 0 || !platform.IsWSL() {
		return out
	}

	// Single tasklist.exe call covers every /proc miss in the batch.
	names := tasklistResolve(ctx, procMisses)
	for pid, name := range names {
		if name != "" {
			out[pid] = name
		}
	}
	return out
}

// tasklistCallCounter tracks tasklist.exe invocations for tests
// asserting the "no call for Linux PIDs" and "one call per batch"
// contracts. Test-only — production code never reads it.
var tasklistCallCounter atomic.Int64

// resetTasklistCallCount and tasklistCallCount are test helpers exposed
// in the same file so the package_test.go discipline isn't disturbed.
// They are not part of the public API.
func resetTasklistCallCount() { tasklistCallCounter.Store(0) }
func tasklistCallCount() int  { return int(tasklistCallCounter.Load()) }

// tasklistResolve invokes tasklist.exe once for the given PIDs and
// returns a map of resolved names. Returns nil on any failure (missing
// binary, exec error, parse error). Per-call — no long-lived cache.
//
// We pass the PID filter list to tasklist.exe directly (/fi "PID eq N")
// when there's a single PID; for batches we issue a single unfiltered
// tasklist call and intersect with the requested PID set. The
// unfiltered call returns ~hundreds of rows on a typical Windows
// host but parses in <5ms — much cheaper than N filtered calls.
func tasklistResolve(ctx context.Context, pids []int) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	exe, err := exec.LookPath("tasklist.exe")
	if err != nil {
		return nil
	}
	tasklistCallCounter.Add(1)

	args := []string{"/fo", "csv", "/nh"}
	// Single-PID: use /fi "PID eq N" — much smaller output, faster parse.
	// Batch: skip /fi and parse the full table in one pass.
	if len(pids) == 1 {
		args = append(args, "/fi", fmt.Sprintf("PID eq %d", pids[0]))
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	all := parseTasklistCSV(output)
	if len(pids) == 1 {
		// /fi already narrowed; return as-is.
		return all
	}
	// Batch path: filter to requested PIDs only.
	wanted := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		wanted[pid] = struct{}{}
	}
	out := make(map[int]string, len(pids))
	for pid, name := range all {
		if _, ok := wanted[pid]; ok {
			out[pid] = name
		}
	}
	return out
}

// parseTasklistCSV parses tasklist.exe /fo csv /nh output into a
// PID -> image-name map. Each row is comma-separated with quoted
// fields:
//
//	"chrome.exe","12345","Console","1","45,000 K"
//
// Field 0 is the image name, field 1 is the PID. Other fields are
// ignored. Malformed rows are dropped silently — a single bad line
// must not poison the batch. Returns an empty (non-nil) map on
// nil/empty input so callers can index it safely.
func parseTasklistCSV(data []byte) map[int]string {
	out := make(map[int]string)
	if len(data) == 0 {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := platform.SplitTasklistCSVLine(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 || name == "" {
			continue
		}
		out[pid] = name
	}
	return out
}

// PortOwner is a single listening TCP port and the process that owns it.
// PID/Name are zero/empty when the owner could not be resolved (e.g. a socket
// owned by another uid whose /proc/<pid>/fd we cannot read). Windows is true
// for owners surfaced from the WSL netstat.exe fallback.
type PortOwner struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	Windows bool   `json:"windows"`
}

// ListListeningPorts enumerates every TCP port in the LISTEN state along with
// its owning process. On Linux it reads /proc/net/tcp{,6} once for the port set
// and walks /proc/*/fd once to attribute owners; on macOS it shells to lsof.
// Under WSL it additionally folds in Windows-side listeners (netstat.exe) for
// ports not already owned by a Linux process — the same visibility contract as
// FindPIDsByPort. Returns ports sorted ascending. Best-effort: returns whatever
// it can read, nil on total failure.
func ListListeningPorts(ctx context.Context) []PortOwner {
	if runtime.GOOS == "linux" {
		return listListeningPortsProc(ctx)
	}
	return listListeningPortsLsof(ctx)
}

// listListeningPortsProc is the Linux implementation of ListListeningPorts.
func listListeningPortsProc(ctx context.Context) []PortOwner {
	// inode -> port for every LISTEN socket.
	inodePort := make(map[string]int)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			inode, port, ok := parseProcNetTCPLine(line)
			if !ok {
				continue
			}
			inodePort[inode] = port
		}
	}

	// Single /proc/*/fd walk to map ports to owning PIDs.
	portPID := make(map[int]int)
	if len(inodePort) > 0 {
		entries, err := os.ReadDir("/proc")
		if err == nil {
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
					if err != nil || !strings.HasPrefix(link, "socket:[") {
						continue
					}
					inode := link[8 : len(link)-1]
					if port, ok := inodePort[inode]; ok {
						if _, seen := portPID[port]; !seen {
							portPID[port] = pid
						}
					}
				}
			}
		}
	}

	seenPort := make(map[int]bool, len(inodePort))
	var out []PortOwner
	for _, port := range inodePort {
		if seenPort[port] {
			continue
		}
		seenPort[port] = true
		po := PortOwner{Port: port}
		if pid, ok := portPID[port]; ok {
			po.PID = pid
			po.Name = procNameLinux(pid)
		}
		out = append(out, po)
	}

	// NOTE: WSL Windows-side enumeration (netstat.exe) is deliberately NOT done
	// here. ListListeningPorts is polled on the overview's 2s status tick, and
	// shelling to netstat.exe + tasklist.exe every tick stalled the WSL VM
	// (~100-150ms each). Windows-side listeners are host noise the overview
	// hides by default anyway; conflict detection on declared ports still
	// reaches Windows owners via FindPIDsByPort's on-demand fallback.
	sortPortOwners(out)
	return out
}

// parseNetstatExeAllListeners parses `netstat.exe -ano` output into one
// PortOwner per LISTENING TCP port (deduped across the IPv4/IPv6 rows a single
// process holds). Malformed rows are dropped.
func parseNetstatExeAllListeners(output []byte) []PortOwner {
	var out []PortOwner
	seen := make(map[int]bool)
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
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
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

// listListeningPortsLsof is the macOS implementation of ListListeningPorts.
func listListeningPortsLsof(ctx context.Context) []PortOwner {
	output, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	portRe := regexp.MustCompile(`:(\d+)\s+\(LISTEN\)`)
	seen := make(map[int]bool)
	var out []PortOwner
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		m := portRe.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		port, err := strconv.Atoi(m[1])
		if err != nil || port <= 0 || seen[port] {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		seen[port] = true
		out = append(out, PortOwner{Port: port, PID: pid, Name: fields[0]})
	}
	sortPortOwners(out)
	return out
}

// sortPortOwners orders owners by ascending port for stable display.
func sortPortOwners(owners []PortOwner) {
	sort.Slice(owners, func(i, j int) bool { return owners[i].Port < owners[j].Port })
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
