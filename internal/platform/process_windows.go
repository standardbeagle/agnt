//go:build windows

package platform

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Scan returns all running processes on Windows.
func Scan() ([]ProcInfo, error) {
	return scanWindows()
}

// scanWindows enumerates processes from a Toolhelp snapshot: one kernel call,
// no child process.
//
// It used to run `tasklist /fo CSV /nh` and parse the output. That had two
// defects. tasklist takes from half a second to several seconds and ran with
// no deadline and no way to cancel, so the daemon's autostart worker (which
// scans on every autostart, collectManagedPIDs) could still be parked in it
// after Daemon.Stop had returned. And tasklist reports no parent PID, so
// every ProcInfo.PPID was 0: the parent-chain walks that protect a managed
// process's children from port cleanup found no children on Windows.
//
// Command and Cmdline are both the image name ("node.exe"), as before; the
// full command line and the working directory need a per-process handle and
// are not read here.
func scanWindows() ([]ProcInfo, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	var procs []ProcInfo
	for {
		// PID 0 is the System Idle Process, a placeholder with no image.
		if entry.ProcessID != 0 {
			name := windows.UTF16ToString(entry.ExeFile[:])
			procs = append(procs, ProcInfo{
				PID:     int(entry.ProcessID),
				PPID:    int(entry.ParentProcessID),
				Command: name,
				Cmdline: name,
			})
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return procs, nil
			}
			return nil, fmt.Errorf("process snapshot: %w", err)
		}
	}
}

// killPID identifies pid now and terminates that process.
//
// It used to open the PID with PROCESS_TERMINATE and terminate whatever held
// it, with no identity check at all: a caller holding a PID from an earlier
// scan killed whichever process had been given that PID since.
func killPID(pid int, gracefulTimeout int) error {
	if pid <= 1 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	id, ok := identifyProcess(pid)
	if !ok {
		// As on Unix: no identity, no signal, whether or not the PID is held.
		return fmt.Errorf("cannot verify pid %d identity before terminating", pid)
	}
	return killProcess(id, gracefulTimeout)
}

// winKillAccess is what a handle needs to be checked, terminated and waited on.
const winKillAccess = windows.PROCESS_TERMINATE | windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE

// winExitWait bounds the wait for a terminated process to finish exiting.
// TerminateProcess only starts the termination; the process's handles, and so
// any port it listens on, are released when it has exited.
const winExitWait = 5 * time.Second

// identifyProcess reads the creation time of the process holding pid. It
// works for a process that has exited while someone still holds a handle to
// it, because until that handle closes the PID is still that process's.
func identifyProcess(pid int) (ProcessIdentity, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ProcessIdentity{}, false
	}
	defer windows.CloseHandle(h)
	birth, ok := handleBirth(h)
	if !ok {
		return ProcessIdentity{}, false
	}
	return ProcessIdentity{PID: pid, Birth: birth}, true
}

// pidHeld reports whether any process, running or not yet released, holds pid.
func pidHeld(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	windows.CloseHandle(h)
	return true
}

// winProc is an open, identity-checked handle. While it is open the PID
// cannot be given to another process, so everything done through it — the
// kill, and trusting parent links that name its PID — is about the process
// that was checked.
type winProc struct {
	id ProcessIdentity
	h  windows.Handle
}

func (p *winProc) identity() ProcessIdentity { return p.id }

func (p *winProc) release() { windows.CloseHandle(p.h) }

// kill terminates the process through the checked handle and waits for it to
// exit. gracefulTimeout is not used: Windows has no SIGTERM for a process
// that is not in our console group.
func (p *winProc) kill(int) error {
	if handleExited(p.h) {
		return nil
	}
	if err := windows.TerminateProcess(p.h, 1); err != nil {
		if handleExited(p.h) {
			return nil // it exited on its own first
		}
		return fmt.Errorf("terminate pid %d: %w", p.id.PID, err)
	}
	ev, err := windows.WaitForSingleObject(p.h, uint32(winExitWait/time.Millisecond))
	if err != nil || ev != uint32(windows.WAIT_OBJECT_0) {
		return fmt.Errorf("pid %d still running %s after TerminateProcess", p.id.PID, winExitWait)
	}
	return nil
}

// openChecked opens id.PID and verifies the creation time. (nil, nil) means no
// process holds the PID.
func openChecked(id ProcessIdentity) (*winProc, error) {
	h, err := windows.OpenProcess(winKillAccess, false, uint32(id.PID))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil, nil
		}
		return nil, fmt.Errorf("open pid %d: %w", id.PID, err)
	}
	birth, ok := handleBirth(h)
	if !ok {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("read creation time of pid %d", id.PID)
	}
	if birth != id.Birth {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("pid %d: %w", id.PID, ErrProcessIdentityChanged)
	}
	return &winProc{id: id, h: h}, nil
}

// killProcess checks and terminates through one handle: no window exists in
// which the PID could change hands between the two.
func killProcess(id ProcessIdentity, _ int) error {
	p, err := openChecked(id)
	if err != nil || p == nil {
		return err
	}
	defer p.release()
	return p.kill(0)
}

// handleParentPID returns the parent PID recorded for the process behind h.
// Windows never updates it, so it may name a process that has exited, and a
// PID since reused; callers pair it with creation times.
func handleParentPID(h windows.Handle) (int, bool) {
	var pbi windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), nil); err != nil {
		return 0, false
	}
	return int(pbi.InheritedFromUniqueProcessId), true
}

// processTreeHost walks a Windows tree through open handles. A member's
// handle stays open for the whole kill, so its PID cannot be reused while
// its children are being found: a process whose recorded parent is that PID
// and which was created after the member is the member's child.
//
// A Job Object would be the other way to reach the tree, but one cannot be
// opened from a process, and a process found in a job may share it with
// unrelated processes (terminals and IDEs run everything in one), so
// terminating the job is not a tree kill.
func processTreeHost() treeHost {
	return treeHost{
		scan: Scan,
		root: func(id ProcessIdentity) (treeMember, error) {
			p, err := openChecked(id)
			if err != nil || p == nil {
				return nil, err
			}
			return p, nil
		},
		child: func(pid int, parent ProcessIdentity) treeMember {
			h, err := windows.OpenProcess(winKillAccess, false, uint32(pid))
			if err != nil {
				return nil
			}
			ppid, ok := handleParentPID(h)
			birth, ok2 := handleBirth(h)
			if !ok || !ok2 || ppid != parent.PID || !birthNotBefore(birth, parent.Birth) {
				windows.CloseHandle(h)
				return nil
			}
			return &winProc{id: ProcessIdentity{PID: pid, Birth: birth}, h: h}
		},
	}
}

// ScanWindows is a no-op on native Windows (already covered by Scan).
func ScanWindows() ([]ProcInfo, error) {
	return nil, nil
}

// IsWSL returns false on native Windows. WSL processes run as Linux binaries
// and use the unix build of this package.
func IsWSL() bool {
	return false
}

// ShouldUseWindowsShell always returns true on native Windows. Path is
// ignored — the platform shell is cmd.exe regardless of whether the
// script lives on the C: drive, a UNC share, or anywhere else. The
// signature matches the unix variant so callers can stay
// platform-agnostic.
func ShouldUseWindowsShell(_ string) bool {
	return true
}

// ShouldUseWindowsCommand always selects cmd.exe on native Windows.
func ShouldUseWindowsCommand(_ string) bool { return true }
