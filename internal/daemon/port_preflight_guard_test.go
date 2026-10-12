package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// forceWindowsPIDsLocal runs the test as if on native Windows (or not), where
// the port probe reports every holder on the Windows side. No t.Parallel() in
// callers: this swaps a package variable.
func forceWindowsPIDsLocal(t *testing.T, local bool) {
	t.Helper()
	prev := windowsPIDsAreLocal
	windowsPIDsAreLocal = local
	t.Cleanup(func() { windowsPIDsAreLocal = prev })
}

func TestProtectedPortHolders(t *testing.T) {
	const self, managed, stranger = 100, 200, 300
	isManaged := func(pid int) bool { return pid == managed }

	t.Run("native Windows protects self and managed PIDs on the windows side", func(t *testing.T) {
		forceWindowsPIDsLocal(t, true)
		got := protectedPortHolders(nil, []int{self, managed, stranger}, self, isManaged)
		if len(got) != 2 || got[0] != self || got[1] != managed {
			t.Fatalf("protected = %v, want [%d %d]", got, self, managed)
		}
	})

	t.Run("WSL windows-side PIDs are another namespace and never match ours", func(t *testing.T) {
		forceWindowsPIDsLocal(t, false)
		if got := protectedPortHolders(nil, []int{self, managed}, self, isManaged); len(got) != 0 {
			t.Fatalf("protected = %v, want none: a Windows-kernel PID equal to one of our Linux PIDs is a coincidence", got)
		}
		if got := protectedPortHolders([]int{self, stranger}, []int{managed}, self, isManaged); len(got) != 1 || got[0] != self {
			t.Fatalf("protected = %v, want [%d]", got, self)
		}
	})
}

// On native Windows a managed process holding its own declared port is not a
// conflict. Before the fix the managed filter skipped the Windows side, so
// auto-kill targeted the daemon's own children.
func TestDetectPortConflicts_NativeWindowsManagedPIDSkipped(t *testing.T) {
	forceWindowsPIDsLocal(t, true)
	scripts := map[string]*config.ScriptConfig{"api": {Ports: []int{19811}, Autostart: true}}
	probe := func(context.Context, int) ([]int, []int) { return nil, []int{4242} }

	if got := detectPortConflicts(context.Background(), scripts, map[int]bool{4242: true}, probe); len(got) != 0 {
		t.Fatalf("managed holder reported as a conflict: %+v", got)
	}
	got := detectPortConflicts(context.Background(), scripts, map[int]bool{1: true}, probe)
	if len(got) != 1 || len(got[0].WindowsPIDs) != 1 || got[0].WindowsPIDs[0] != 4242 {
		t.Fatalf("unmanaged holder must still be a conflict, got %+v", got)
	}
}

// The kill half of the same guard: a conflict that names this process on the
// Windows side must be refused, loudly, with the process left alive. Without
// the guard this test kills its own binary.
func TestKillPortBlockers_NativeWindowsRefusesSelf(t *testing.T) {
	forceWindowsPIDsLocal(t, true)
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	results := killPortBlockers(context.Background(), pm, nil, []PortConflict{{
		ScriptName: "api", Port: port, PIDs: []int{os.Getpid()}, WindowsPIDs: []int{os.Getpid()},
	}})
	if len(results) != 1 || results[0].Killed {
		t.Fatalf("self-held port reported as cleared: %+v", results)
	}
	if !strings.Contains(results[0].Error, "kill skipped") {
		t.Fatalf("refusal must be reported, got error %q", results[0].Error)
	}
}

// The real scan + kill path on a host whose probe reports holders on the
// Windows side. The Unix twin is TestKillPortHoldersGuarded_ProtectsSelf.
func TestKillPortHoldersGuarded_ProtectsSelfOnWindowsSide(t *testing.T) {
	if !windowsPIDsAreLocal {
		t.Skip("native Windows only: elsewhere the probe reports this process on the Linux side")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	killed, protected, err := killPortHoldersGuarded(context.Background(), pm, port)
	if err != nil {
		t.Fatal(err)
	}
	if len(killed) != 0 {
		t.Fatalf("killed %v while this process holds the port", killed)
	}
	if len(protected) != 1 || protected[0] != os.Getpid() {
		t.Fatalf("protected = %v, want this process (%d)", protected, os.Getpid())
	}
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("self-held listener did not survive the guarded kill: %v", err)
	}
	conn.Close()
}

// TestWithExpectedPorts_CoversLaunchPathPorts proves the autostart conflict
// scan sees every port the launch pre-flight would clean up — not only the
// declared ones — so "prompt" and "fail" get to rule on all of them.
func TestWithExpectedPorts_CoversLaunchPathPorts(t *testing.T) {
	d, _, tmpDir := newHubProcTestDaemon(t)
	scripts := map[string]*config.ScriptConfig{
		"dev":  {Run: "vite --port 5199", Ports: []int{5198}},
		"api":  {Command: "server", Args: []string{"--port=5197"}},
		"none": {Command: "echo", Args: []string{"hi"}},
	}

	got := d.withExpectedPorts(scripts, nil, tmpDir)

	want := map[string][]int{"dev": {5198, 5199}, "api": {5197}, "none": nil}
	for name, ports := range want {
		if fmt.Sprint(got[name].Ports) != fmt.Sprint(ports) {
			t.Errorf("%s: scan ports = %v, want %v", name, got[name].Ports, ports)
		}
	}
	if len(scripts["dev"].Ports) != 1 {
		t.Errorf("the config's own Ports were mutated: %v", scripts["dev"].Ports)
	}

	// The expanded set is what detection reports on.
	probe := func(_ context.Context, port int) ([]int, []int) {
		if port == 5199 {
			return []int{4242}, nil
		}
		return nil, nil
	}
	conflicts := detectPortConflicts(context.Background(), got, map[int]bool{}, probe)
	if len(conflicts) != 1 || conflicts[0].Port != 5199 || conflicts[0].ScriptName != "dev" {
		t.Fatalf("a holder on the command-line port must be reported, got %+v", conflicts)
	}
}

// TestManagedTreeChecker_ProtectsDescendants: the listener is usually a child
// of the managed PID (cmd /c → node, dotnet watch → dotnet), so the kill guard
// must treat the whole managed tree as protected, not just its root.
func TestManagedTreeChecker_ProtectsDescendants(t *testing.T) {
	// No t.Parallel(): starts a real process and swaps a package variable.
	d, _, tmpDir := newHubProcTestDaemon(t)
	command, args := stayAliveExec()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	proc, err := d.StartScript(ctx, StartScriptConfig{
		ProcessID: makeProcessID(tmpDir, "root"), ProjectPath: tmpDir, WorkingDir: tmpDir,
		Command: command, Args: args,
	})
	if err != nil {
		t.Fatalf("StartScript: %v", err)
	}
	root := proc.Process.PID()

	const child, grandchild, stranger, looped = 7000001, 7000002, 7000003, 7000004
	prev := scanProcs
	scanProcs = func() ([]platform.ProcInfo, error) {
		return []platform.ProcInfo{
			{PID: root, PPID: os.Getpid()},
			{PID: child, PPID: root},
			{PID: grandchild, PPID: child},
			{PID: stranger, PPID: 1},
			{PID: looped, PPID: looped},
		}, nil
	}
	t.Cleanup(func() { scanProcs = prev })

	isManaged := managedTreeChecker(d.hub.ProcessManager())
	for pid, want := range map[int]bool{root: true, child: true, grandchild: true, stranger: false, looped: false, 7999999: false} {
		if got := isManaged(pid); got != want {
			t.Errorf("isManaged(%d) = %v, want %v", pid, got, want)
		}
	}

	forceWindowsPIDsLocal(t, true)
	if got := protectedPortHolders(nil, []int{grandchild, stranger}, os.Getpid(), isManaged); len(got) != 1 || got[0] != grandchild {
		t.Fatalf("protected = %v, want only the managed grandchild %d", got, grandchild)
	}
}
