package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// PortConflict describes an unmanaged process blocking a declared port.
//
// PIDs is the union of LinuxPIDs + WindowsPIDs. The split exists so the
// kill path can route Linux-side PIDs through ProcessManager (syscall.Kill
// + process-group escalation) and Windows-side PIDs through
// platform.KillWindowsPID (taskkill.exe interop). On non-WSL hosts
// WindowsPIDs is always nil.
type PortConflict struct {
	ScriptName  string `json:"script_name"`
	Port        int    `json:"port"`
	PIDs        []int  `json:"pids"`
	ProcessName string `json:"process_name,omitempty"`

	// LinuxPIDs are sourced from /proc/net/tcp (Linux) or lsof (macOS).
	// Killable via syscall.Kill / ProcessManager.KillProcessByPort.
	LinuxPIDs []int `json:"linux_pids,omitempty"`

	// WindowsPIDs are sourced from netstat.exe via WSL interop. They live
	// in the Windows kernel namespace and are unreachable from syscall.Kill;
	// they require platform.KillWindowsPID (taskkill.exe).
	WindowsPIDs []int `json:"windows_pids,omitempty"`
}

// portProbe resolves which PIDs hold a given port, tagged by OS side. It is
// injected so the conflict-classification logic can be unit-tested without
// depending on the real (and, on WSL, intermittent) /proc + netstat.exe scan —
// the source of the TestDetectPortConflicts_* flakes. Production passes
// config.FindPIDsByPortTagged.
type portProbe func(ctx context.Context, port int) (linuxPIDs, windowsPIDs []int)

// detectPortConflicts scans all declared ports from autostart scripts and
// returns conflicts where unmanaged processes hold those ports. Each
// conflict's PIDs are partitioned into LinuxPIDs and WindowsPIDs so the
// kill path can route them to the correct termination primitive. The probe
// is injected (see portProbe).
func detectPortConflicts(ctx context.Context, scripts map[string]*config.ScriptConfig, managedPIDs map[int]bool, probe portProbe) []PortConflict {
	var conflicts []PortConflict

	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sc := scripts[name]
		for _, port := range sc.Ports {
			linuxPIDs, windowsPIDs := probe(ctx, port)
			if len(linuxPIDs) == 0 && len(windowsPIDs) == 0 {
				continue
			}
			unmanagedLinux := filterUnmanaged(linuxPIDs, managedPIDs)
			// Under WSL, Windows-side PIDs are never in our managed set (we
			// only register Linux PIDs with the ProcessManager), so the
			// "managed" filter does not apply to them. On native Windows they
			// ARE our namespace — every managed process is a Windows PID — so
			// they get the same filter the Linux side does.
			if windowsPIDsAreLocal {
				windowsPIDs = filterUnmanaged(windowsPIDs, managedPIDs)
			}
			if len(unmanagedLinux) == 0 && len(windowsPIDs) == 0 {
				continue
			}
			all := make([]int, 0, len(unmanagedLinux)+len(windowsPIDs))
			all = append(all, unmanagedLinux...)
			all = append(all, windowsPIDs...)
			procName := config.ProcessNameByPID(all[0])
			conflicts = append(conflicts, PortConflict{
				ScriptName:  name,
				Port:        port,
				PIDs:        all,
				ProcessName: procName,
				LinuxPIDs:   unmanagedLinux,
				WindowsPIDs: windowsPIDs,
			})
		}
	}
	return conflicts
}

// filterUnmanaged drops any PIDs the ProcessManager already owns. Returns
// nil rather than an empty slice when nothing remains, matching the
// existing detect path's nil-vs-empty semantics.
func filterUnmanaged(pids []int, managedPIDs map[int]bool) []int {
	if len(pids) == 0 {
		return nil
	}
	if managedPIDs == nil {
		return pids
	}
	var out []int
	for _, pid := range pids {
		if managedPIDs[pid] {
			continue
		}
		out = append(out, pid)
	}
	return out
}

// windowsPIDsAreLocal reports whether the PIDs a port probe tags as
// Windows-side live in this process's own PID namespace. True on native
// Windows, where the probe returns every holder in the windows slice and those
// PIDs include the daemon itself and everything it manages. False under WSL
// (GOOS=linux), where they belong to the Windows kernel and a numeric match
// with one of our Linux PIDs is a coincidence, never an identity.
//
// A variable so the native-Windows classification is testable on any host.
var windowsPIDsAreLocal = runtime.GOOS == "windows"

// protectedPortHolders returns the port holders a kill must never reach: this
// process and anything the ProcessManager owns. It reads both sides of the
// probe result, because which side carries our own PIDs depends on the host
// (see windowsPIDsAreLocal) — checking only the Linux side left the guard
// inert on native Windows.
func protectedPortHolders(linuxPIDs, windowsPIDs []int, self int, isManaged func(pid int) bool) []int {
	candidates := linuxPIDs
	if windowsPIDsAreLocal {
		candidates = append(append([]int(nil), linuxPIDs...), windowsPIDs...)
	}
	var protected []int
	for _, pid := range candidates {
		if pid == self || isManaged(pid) {
			protected = append(protected, pid)
		}
	}
	return protected
}

// scanProcs lists live processes with their parents. A variable so the
// descendant walk in managedTreeChecker is testable without real processes.
var scanProcs = platform.Scan

// managedTreeChecker returns a predicate that is true for a PID the
// ProcessManager owns or any descendant of one. A dev server is rarely the PID
// the manager started: `cmd /c`, `npm run`, `dotnet watch` and friends leave the
// listener one or more levels down, so matching only the managed PID itself
// leaves the process that actually holds the port unprotected. The process
// table is scanned once, on the first PID that is not directly managed.
func managedTreeChecker(pm *goprocess.ProcessManager) func(pid int) bool {
	var parents map[int]int
	return func(pid int) bool {
		if pm == nil {
			return false
		}
		if pm.IsManagedPID(pid) {
			return true
		}
		if parents == nil {
			procs, _ := scanProcs()
			parents = make(map[int]int, len(procs))
			for _, p := range procs {
				parents[p.PID] = p.PPID
			}
		}
		visited := map[int]bool{pid: true}
		for ppid, ok := parents[pid]; ok && ppid > 1 && !visited[ppid]; ppid, ok = parents[ppid] {
			if pm.IsManagedPID(ppid) {
				return true
			}
			visited[ppid] = true
		}
		return false
	}
}

// killPortHoldersGuarded kills whatever holds port via pm.KillProcessByPort —
// unless the port is held by this process itself, by a PID the
// ProcessManager owns, or by a descendant of one. KillProcessByPort re-discovers holders at kill time and
// kills them all (it has no exclusion list), so any self/managed filtering a
// caller did on an earlier scan is void by the time the kill fires. This guard
// re-scans immediately before the kill and refuses to fire when a protected
// PID holds the port, returning the protected PIDs so the caller can surface
// the skip loudly (Silent Failure Prohibition). Without it, a proxy listener
// living inside the daemon process — or a managed dev server sharing the port —
// gets SIGTERM'd by our own cleanup.
func killPortHoldersGuarded(ctx context.Context, pm *goprocess.ProcessManager, port int) (killed, protected []int, err error) {
	linuxPIDs, windowsPIDs := config.FindPIDsByPortTagged(ctx, port)
	protected = protectedPortHolders(linuxPIDs, windowsPIDs, os.Getpid(), managedTreeChecker(pm))
	if len(protected) > 0 {
		return nil, protected, nil
	}
	killed, err = pm.KillProcessByPort(ctx, port)
	return killed, nil, err
}

// KillResult reports what happened for each conflict.
type KillResult struct {
	PortConflict
	Killed bool   `json:"killed"`
	Error  string `json:"error,omitempty"`
}

// killPortBlockers kills processes blocking declared ports.
//
// Linux-side PIDs go through the ProcessManager's full escalation path
// (process groups + descendants). Windows-side PIDs (sourced from the
// netstat.exe fallback on WSL) go through platform.KillWindowsPID, which
// shells to taskkill.exe /F /PID — required because Windows PIDs live in
// a different kernel namespace and syscall.Kill returns ESRCH for them.
//
// Per-PID kill failures on the Windows path are surfaced as warning-severity
// alerts via eventHub (when non-nil), satisfying the Silent Failure
// Prohibition rule. A nil eventHub is allowed for tests and for callers
// that prefer to inspect the returned KillResult.Error directly.
//
// Verifies each port is free after kill.
func killPortBlockers(ctx context.Context, pm *goprocess.ProcessManager, eventHub *EventHub, conflicts []PortConflict) []KillResult {
	results := make([]KillResult, len(conflicts))
	isManaged := managedTreeChecker(pm)

	for i, c := range conflicts {
		results[i].PortConflict = c

		var errs []string

		// Back-compat: callers that built PortConflict directly (legacy
		// shape) populate PIDs without splitting into Linux/Windows.
		// Treat the unsplit case as "all PIDs are Linux-side" so the
		// existing behavior is preserved for non-WSL conflicts. New
		// callers from detectPortConflicts populate LinuxPIDs and
		// WindowsPIDs explicitly.
		linuxPresent := len(c.LinuxPIDs) > 0 || (len(c.LinuxPIDs) == 0 && len(c.WindowsPIDs) == 0 && len(c.PIDs) > 0)

		// Linux PIDs: ProcessManager handles them via syscall.Kill +
		// process-group escalation, guarded against self/managed holders
		// (the kill re-discovers PIDs at fire time, so the detect-phase
		// managed filter alone is not enough). No-op for Windows-only
		// conflicts. Skip when there are no Linux PIDs to avoid the
		// redundant scan.
		if linuxPresent {
			_, protected, err := killPortHoldersGuarded(ctx, pm, c.Port)
			if err != nil {
				errs = append(errs, fmt.Sprintf("linux kill failed: %v", err))
			}
			if len(protected) > 0 {
				msg := fmt.Sprintf("port %d held by daemon or managed process (PIDs %v), kill skipped", c.Port, protected)
				errs = append(errs, msg)
				if eventHub != nil {
					eventHub.Deliver("warning", msg)
				}
			}
		}

		// Windows PIDs: per-PID taskkill.exe interop. Surface each
		// failure as a visible warning event AND record it in the
		// KillResult so the autostart-prompt path can show the user
		// what went wrong. Empty WindowsPIDs slice is the no-op case.
		//
		// On native Windows these PIDs are in our own namespace, so the
		// self/managed guard applies to them exactly as it does to the Linux
		// side above: the conflict was classified on an earlier scan, and a
		// PID that is now the daemon or a managed process must not be killed.
		for _, pid := range c.WindowsPIDs {
			if windowsPIDsAreLocal && (pid == os.Getpid() || isManaged(pid)) {
				msg := fmt.Sprintf("port %d held by daemon or managed process (PID %d), kill skipped", c.Port, pid)
				errs = append(errs, msg)
				if eventHub != nil {
					eventHub.Deliver("warning", msg)
				}
				continue
			}
			if err := platform.KillWindowsPID(pid); err != nil {
				msg := fmt.Sprintf("port %d: failed to kill Windows-side pid %d: %v", c.Port, pid, err)
				errs = append(errs, msg)
				if eventHub != nil {
					eventHub.Deliver("warning", msg)
				}
			}
		}

		if waitPortFree(c.Port, 2*time.Second) {
			results[i].Killed = true
			// Even when the port is free, surface kill errors so the
			// caller can see partial failures (e.g. one of three PIDs
			// died on its own while another was access-denied).
			if len(errs) > 0 {
				results[i].Error = joinErrs(errs)
			}
		} else {
			if len(errs) == 0 {
				errs = append(errs, fmt.Sprintf("port %d still in use after kill", c.Port))
			}
			results[i].Error = joinErrs(errs)
		}
	}

	return results
}

// joinErrs concatenates kill errors with a "; " separator for the
// human-readable KillResult.Error field. Single-element slices return
// the message as-is.
func joinErrs(errs []string) string {
	if len(errs) == 1 {
		return errs[0]
	}
	out := errs[0]
	for _, e := range errs[1:] {
		out += "; " + e
	}
	return out
}

// waitPortFree polls until port is free or timeout expires.
func waitPortFree(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
