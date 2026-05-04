//go:build !windows && !linux && !darwin

// Stub orphan-pgid implementation for unix platforms that have neither
// /proc (linux) nor sysctl KERN_PROC_ALL with the kinfo_proc shape we
// rely on (darwin). FreeBSD, OpenBSD, NetBSD, illumos, etc. compile
// against this file.
//
// All public entry points return empty results and `readPGID` falls back
// to syscall.Getpgid. The daemon path that consumes orphan scans treats
// an empty result as "no orphans found", which is correct: on platforms
// without a process table walker we cannot detect orphans, but we also
// will not falsely reap anything.

package platform

// ScanOrphanedPGIDs is a no-op on unsupported unixes. Always nil.
//
// Daemon-startup orphan-pgid cleanup degrades to "no detection" on these
// platforms; session-pgid containment for the live process tree still
// works via KillSessionPGID (which uses syscall.Kill(-pgid, sig)).
func ScanOrphanedPGIDs(callerUID int, excludePGIDs map[int]bool) []OrphanPGID {
	return nil
}

// ReadProcCmdline returns "" on unsupported unixes. The daemon-startup
// ownership gate consults this for evidence; an empty result means the
// gate cannot positively identify ownership and will conservatively skip
// the candidate.
func ReadProcCmdline(pid int) string { return "" }

// ReadProcCwd returns "" on unsupported unixes. Same conservative-gate
// rationale as ReadProcCmdline.
func ReadProcCwd(pid int) string { return "" }

// WalkParents returns nil on unsupported unixes. The daemon-startup
// ownership gate iterates the returned chain; nil means "no ancestors
// found", which prevents the gate from positively identifying ownership.
func WalkParents(pid int) []AncestorInfo { return nil }

// readPGID is defined in sessionpgid_unix.go (build-tagged !windows). The
// /proc-driven body returns 0 on platforms without /proc; membersOfPGID
// falls back to syscall.Getpgid in that case.
