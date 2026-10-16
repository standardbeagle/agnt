//go:build windows

package platform

import "fmt"

// KillWindowsPID on native Windows identifies pid now and kills it with its
// whole tree (KillProcessTree), matching the WSL variant's taskkill /T. It
// used to terminate only the named process, so the children of a port holder
// outlived the kill on native Windows only.
//
// It exists so code written for the WSL → Windows interop path compiles
// here. A native-Windows caller holding a PID from an earlier scan must kill
// through the ProcessIdentity it captured at that scan, with KillProcessTree.
func KillWindowsPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("kill windows pid: invalid pid %d", pid)
	}
	id, ok := IdentifyProcess(pid)
	if !ok {
		if !pidHeld(pid) {
			return fmt.Errorf("kill windows pid %d: no such process", pid)
		}
		return fmt.Errorf("kill windows pid %d: cannot read its identity", pid)
	}
	if err := KillProcessTree(id, 0); err != nil {
		return fmt.Errorf("kill windows pid %d: %w", pid, err)
	}
	return nil
}
