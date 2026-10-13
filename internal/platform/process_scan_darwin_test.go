//go:build darwin

package platform

import "testing"

// Scan on macOS is built from a sysctl snapshot plus a per-PID argv read.
// These tests drive it with injected sources; they compile only for darwin.
func TestScanWith_BuildsProcInfo(t *testing.T) {
	src := func() ([]darwinProcRow, error) {
		return []darwinProcRow{
			{PID: 10, PPID: 1, PGID: 10, UID: 501, Comm: "node"},
			{PID: 11, PPID: 10, PGID: 10, UID: 0, Comm: "launchd"},
			{PID: 0, PPID: 0},
		}, nil
	}
	cmdline := func(pid int) string {
		if pid == 10 {
			return "/usr/local/bin/node server.js"
		}
		return "" // the kernel refuses argv for another user's process
	}
	procs, err := scanWith(src, cmdline)
	if err != nil {
		t.Fatalf("scanWith: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("got %d procs, want 2 (pid 0 dropped): %+v", len(procs), procs)
	}
	if procs[0].PID != 10 || procs[0].PPID != 1 || procs[0].Command != "node" || procs[0].Cmdline != "/usr/local/bin/node server.js" {
		t.Errorf("own process: %+v", procs[0])
	}
	// Unreadable argv must not drop the row: pgid membership needs the PID.
	if procs[1].PID != 11 || procs[1].Command != "launchd" || procs[1].Cmdline != "launchd" {
		t.Errorf("foreign process: %+v", procs[1])
	}
}

func TestScanWith_PropagatesSourceError(t *testing.T) {
	procs, err := scanWith(func() ([]darwinProcRow, error) { return nil, errFakeSysctl }, func(int) string { return "" })
	if err == nil || procs != nil {
		t.Fatalf("scanWith = %v, %v; want nil and an error", procs, err)
	}
}
