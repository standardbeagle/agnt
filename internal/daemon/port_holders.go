package daemon

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
)

// describePortHolders reads everything the host reports about the processes
// listening on port. Best effort: a holder that vanished or cannot be read is
// still listed by PID so the caller never sees a kill of an unnamed process.
func describePortHolders(ctx context.Context, port int) []platform.ProcDetails {
	linuxPIDs, windowsPIDs := config.FindPIDsByPortTagged(ctx, port)
	out := make([]platform.ProcDetails, 0, len(linuxPIDs)+len(windowsPIDs))
	for _, pid := range linuxPIDs {
		d, ok := platform.DescribePID(pid)
		if !ok {
			d = platform.ProcDetails{PID: pid}
		}
		out = append(out, d)
	}
	// Windows-side PIDs (native Windows, or WSL reaching the host) are not in
	// our /proc; the name is all the batched lookup offers.
	if len(windowsPIDs) > 0 {
		names := config.ProcessNamesByPIDs(ctx, windowsPIDs)
		for _, pid := range windowsPIDs {
			// Under WSL the PID is in the Windows kernel's namespace; reading
			// /proc/<pid> would describe an unrelated Linux process.
			d := platform.ProcDetails{PID: pid}
			if runtime.GOOS == "windows" {
				if got, ok := platform.DescribePID(pid); ok {
					d = got
				}
			}
			if d.Command == "" {
				d.Command = names[pid]
			}
			out = append(out, d)
		}
	}
	return out
}

// portHolderEntries renders the described holders among pids as JSON-ready
// maps, empty fields omitted.
func portHolderEntries(described []platform.ProcDetails, pids []int) []map[string]interface{} {
	want := make(map[int]bool, len(pids))
	for _, p := range pids {
		want[p] = true
	}
	var out []map[string]interface{}
	for _, d := range described {
		if !want[d.PID] {
			continue
		}
		e := map[string]interface{}{"pid": d.PID}
		if d.PPID != 0 {
			e["ppid"] = d.PPID
		}
		if d.Command != "" {
			e["command"] = d.Command
		}
		if d.Cmdline != "" {
			e["cmdline"] = d.Cmdline
		}
		if d.Cwd != "" {
			e["cwd"] = d.Cwd
		}
		if d.User != "" {
			e["uid"] = d.User
		}
		if !d.StartTime.IsZero() {
			e["started_at"] = d.StartTime.UTC().Format(time.RFC3339)
			e["uptime"] = d.Uptime.String()
		}
		if d.RSSBytes > 0 {
			e["memory_bytes"] = d.RSSBytes
		}
		if d.CPUSeconds > 0 {
			e["cpu_seconds"] = d.CPUSeconds
		}
		if d.Threads > 0 {
			e["threads"] = d.Threads
		}
		out = append(out, e)
	}
	return out
}

// formatPortHolders is the human-readable form of the holders in pids, one
// block per process, for error text.
func formatPortHolders(described []platform.ProcDetails, pids []int) string {
	want := make(map[int]bool, len(pids))
	for _, p := range pids {
		want[p] = true
	}
	var b strings.Builder
	for _, d := range described {
		if !want[d.PID] {
			continue
		}
		fmt.Fprintf(&b, "  pid %d", d.PID)
		if d.Command != "" {
			fmt.Fprintf(&b, " %s", d.Command)
		}
		if d.Uptime > 0 {
			fmt.Fprintf(&b, " up %s", d.Uptime)
		}
		if d.RSSBytes > 0 {
			fmt.Fprintf(&b, " mem %.1fMiB", float64(d.RSSBytes)/(1<<20))
		}
		if d.CPUSeconds > 0 {
			fmt.Fprintf(&b, " cpu %.1fs", d.CPUSeconds)
		}
		if d.Cwd != "" {
			fmt.Fprintf(&b, " cwd %s", d.Cwd)
		}
		if d.Cmdline != "" {
			fmt.Fprintf(&b, "\n    %s", d.Cmdline)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
