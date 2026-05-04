//go:build windows

package platform

// KillWindowsPID on native Windows delegates to KillPID, which already uses
// TerminateProcess via the Win32 API. The signature exists so callers
// written for the WSL → Windows interop path compile cross-platform; on
// native Windows there is no namespace mismatch to bridge.
func KillWindowsPID(pid int) error {
	return KillPID(pid, 0)
}
