//go:build darwin

// Darwin-native orphan pgid scanner. macOS has no /proc, so the Linux
// implementation in orphanpgid_unix.go cannot be used. This file provides
// the same ScanOrphanedPGIDs / WalkParents / ReadProcCmdline / ReadProcCwd
// surface area using sysctl(KERN_PROC_ALL) — the canonical macOS API for
// enumerating processes with their pgid, ppid, and uid in a single call.
//
// Architecture:
//
//	┌────────────────────────┐
//	│ ScanOrphanedPGIDs (pub)│  fixed wrapper, calls darwinSysctlSource
//	└──────────┬─────────────┘
//	           │
//	┌──────────▼─────────────┐
//	│ scanOrphanedPGIDsDarwin│  pure classifier, takes procSourceFn
//	│   (pkg-private)        │
//	└──────────┬─────────────┘
//	           │
//	┌──────────▼─────────────┐
//	│ darwinSysctlSource     │  sysctl-backed implementation of procSourceFn
//	└────────────────────────┘
//
// The classifier is pure and exhaustively tested in
// orphanpgid_classify_test.go, which builds on every unix to allow CI on
// Linux to verify the darwin classification logic without an actual macOS
// host. The sysctl source is the only part that needs a real darwin
// runtime to exercise; cross-compilation (GOOS=darwin go build) verifies
// it links and type-checks.

package platform

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ScanOrphanedPGIDs is the darwin entry point matching the linux signature
// in orphanpgid_unix.go. It walks the live process table via sysctl and
// classifies orphan pgids using the same five-rule contract documented on
// the linux variant:
//
//  1. pgid > 1
//  2. pgid not in excludePGIDs
//  3. leader pid (pid == pgid) is NOT in the live snapshot
//  4. at least one live member exists
//  5. every live member is owned by callerUID
//
// Returns nil if sysctl is unavailable or returns no processes.
func ScanOrphanedPGIDs(callerUID int, excludePGIDs map[int]bool) []OrphanPGID {
	return scanOrphanedPGIDsDarwin(callerUID, excludePGIDs, darwinSysctlSource)
}

// darwinSysctlSource queries sysctl KERN_PROC_ALL via golang.org/x/sys/unix
// and returns one row per live process with the fields the orphan
// classifier consumes. Errors propagate to the caller, which treats them
// as "no processes visible" (returns nil).
func darwinSysctlSource() ([]darwinProcRow, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	out := make([]darwinProcRow, 0, len(procs))
	for i := range procs {
		kp := &procs[i]
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		out = append(out, darwinProcRow{
			PID:  pid,
			PPID: int(kp.Eproc.Ppid),
			PGID: int(kp.Eproc.Pgid),
			UID:  int(kp.Eproc.Ucred.Uid),
		})
	}
	return out, nil
}

// ReadProcCmdline returns the cmdline of pid via sysctl KERN_PROCARGS2.
// Matches the linux variant signature; returns "" on any error or when
// the pid is invalid.
//
// KERN_PROCARGS2 layout (per ps(1) source and Apple developer docs):
//
//	int32 argc
//	char  exec_path[]    NUL-terminated
//	char  argv[0][]      NUL-terminated
//	char  argv[1][]      NUL-terminated
//	...
//
// The returned string is the space-joined argv, matching the lossy
// shape ReadProcCmdline returns on linux. The exec_path prefix and any
// alignment padding are skipped.
func ReadProcCmdline(pid int) string {
	if pid <= 0 {
		return ""
	}
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return ""
	}
	// First 4 bytes: argc (host byte order — darwin is little-endian on
	// every supported arch, but use bit ops to stay explicit).
	argc := int(uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24)
	if argc <= 0 {
		return ""
	}
	rest := buf[4:]
	// Skip exec_path: scan to first NUL, then skip any NUL padding.
	i := 0
	for i < len(rest) && rest[i] != 0 {
		i++
	}
	for i < len(rest) && rest[i] == 0 {
		i++
	}
	// Extract argc NUL-terminated strings.
	args := make([]string, 0, argc)
	for a := 0; a < argc && i < len(rest); a++ {
		start := i
		for i < len(rest) && rest[i] != 0 {
			i++
		}
		if i > start {
			args = append(args, string(rest[start:i]))
		}
		if i < len(rest) {
			i++ // skip terminating NUL
		}
	}
	return strings.Join(args, " ")
}

// ReadProcCwd returns the working directory of pid. macOS does not expose
// per-process cwd through sysctl in any practical way (proc_pidinfo with
// PROC_PIDVNODEPATHINFO requires libproc / cgo). We shell out to lsof,
// which is bundled with macOS and returns the cwd reliably:
//
//	lsof -a -d cwd -p <pid> -Fn
//
// This matches the daemon-startup orphan-pgid ownership gate's needs:
// cmdline is the primary evidence, cwd is a secondary signal. Returns ""
// on any error (lsof missing, permission denied, process gone).
func ReadProcCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	cmd := exec.Command("lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// lsof -F output: one field per line, prefixed by tag char. We want
	// the line starting with 'n' (name).
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}

// WalkParents walks the parent chain of pid upward, matching the linux
// variant signature exactly. Each ancestor's ppid is read via sysctl
// KERN_PROC_PID rather than /proc/<pid>/stat.
//
// Stop conditions match the linux variant:
//  1. ppid <= 0
//  2. cur pid <= 0
//  3. sysctl read fails for cur (race: ancestor vanished)
//  4. cycle detected
//  5. depth limit (64)
//
// Returned slice does NOT include pid itself — only strict ancestors.
func WalkParents(pid int) []AncestorInfo {
	if pid <= 1 {
		return nil
	}
	const maxDepth = 64
	visited := make(map[int]bool, 8)
	out := make([]AncestorInfo, 0, 8)

	cur := pid
	for depth := 0; depth < maxDepth; depth++ {
		ppid := readProcPPIDDarwin(cur)
		if ppid <= 0 {
			return out
		}
		if visited[ppid] {
			return out
		}
		visited[ppid] = true

		out = append(out, AncestorInfo{
			PID:     ppid,
			PPID:    readProcPPIDDarwin(ppid),
			Cmdline: ReadProcCmdline(ppid),
			Cwd:     ReadProcCwd(ppid),
		})

		if ppid == 1 {
			return out
		}
		cur = ppid
	}
	return out
}

// readProcPPIDDarwin returns the parent PID of pid via sysctl KERN_PROC_PID.
// Returns 0 on any error or when the process has gone away.
func readProcPPIDDarwin(pid int) int {
	if pid <= 0 {
		return 0
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0
	}
	return int(kp.Eproc.Ppid)
}

// readPGID is defined in sessionpgid_unix.go (build-tagged !windows). On
// darwin the /proc-based body returns 0, which causes membersOfPGID to
// fall back to syscall.Getpgid per-pid — slower than a sysctl-driven
// pass but correct. Optimizing readPGID for darwin via sysctl is a
// separate change, not part of the orphan-scan slice.
