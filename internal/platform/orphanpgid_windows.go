//go:build windows

package platform

// OrphanPGID is defined identically on Windows so cross-platform callers
// compile without build tags. It is never populated because Windows has
// no POSIX process groups -- session containment on Windows is handled by
// Job Objects (see cmd/agnt/run_windows.go and the upcoming Slice C of
// task O9QzO07vM8JB).
type OrphanPGID struct {
	PGID    int
	Members []int
}

// ScanOrphanedPGIDs is a no-op on Windows. Always returns nil.
//
// The stub exists so daemon startup can invoke the scan unconditionally
// without a runtime.GOOS check. Windows session-leak detection is a
// separate slice that will plug in at the same call site.
func ScanOrphanedPGIDs(callerUID int, excludePGIDs map[int]bool) []OrphanPGID {
	return nil
}
