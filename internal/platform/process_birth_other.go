//go:build !linux && !darwin

package platform

// ProcessBirthID is unavailable on platforms where this package does not yet
// expose a non-reusable kernel process birth identifier. Safety-sensitive
// callers must fail closed rather than substitute command-line heuristics.
func ProcessBirthID(pid int) (string, bool) { return "", false }
