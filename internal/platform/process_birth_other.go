//go:build !linux && !darwin && !windows

package platform

// ProcessBirthID is unavailable on platforms where this package does not yet
// expose a non-reusable kernel process birth identifier. Safety-sensitive
// callers must fail closed rather than substitute command-line heuristics.
func ProcessBirthID(pid int) (string, bool) { return "", false }

// processLineage is unavailable for the same reason, so a tree kill here
// reaches no descendants.
func processLineage(pid int) (ppid int, birth string, ok bool) { return 0, "", false }
