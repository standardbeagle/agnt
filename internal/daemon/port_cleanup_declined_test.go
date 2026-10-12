package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
)

// These tests hold the contested port with a listener inside the test process.
// That makes the kill path observable without a victim: an attempted cleanup
// reaches killPortHoldersGuarded, which refuses a self-held port and records
// port_cleanup_failed, while a declined cleanup never gets that far and records
// port_cleanup_declined instead.

func heldPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func startupEventsForPort(d *Daemon, port int) map[string]int {
	events := make(map[string]int)
	for _, e := range d.startupErrorStore.Query(StartupLogFilter{}) {
		if e.Port == port {
			events[e.EventType]++
		}
	}
	return events
}

func TestStartScript_DeclinedPortCleanupNeverReachesTheKill(t *testing.T) {
	// No t.Parallel(): starts real processes.
	d, _, tmpDir := newHubProcTestDaemon(t)
	command, args := stayAliveExec()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	t.Run("declined", func(t *testing.T) {
		port := heldPort(t)
		processID := makeProcessID(tmpDir, "declined")
		if _, err := d.StartScript(ctx, StartScriptConfig{
			ProcessID: processID, ProjectPath: tmpDir, WorkingDir: tmpDir,
			Command: command, Args: args, ExpectedPorts: []int{port},
			SkipPortCleanup: true,
		}); err != nil {
			t.Fatalf("StartScript: %v", err)
		}
		events := startupEventsForPort(d, port)
		if events["port_cleanup_declined"] != 1 {
			t.Errorf("a held port must be reported when its cleanup is declined; events = %v", events)
		}
		if events["port_cleanup_failed"] != 0 || events["port_cleanup"] != 0 {
			t.Errorf("declined cleanup still ran the port kill; events = %v", events)
		}
		if _, ok := d.portKillDeclined.Load(processID); !ok {
			t.Error("the decline must persist for later restarts of this process")
		}
	})

	// Mutation guard: the same launch without the decline does reach the kill,
	// so the assertions above are not vacuous.
	t.Run("not declined", func(t *testing.T) {
		port := heldPort(t)
		processID := makeProcessID(tmpDir, "cleanup")
		d.portKillDeclined.Store(processID, struct{}{}) // stale mark from an earlier start
		if _, err := d.StartScript(ctx, StartScriptConfig{
			ProcessID: processID, ProjectPath: tmpDir, WorkingDir: tmpDir,
			Command: command, Args: args, ExpectedPorts: []int{port},
		}); err != nil {
			t.Fatalf("StartScript: %v", err)
		}
		events := startupEventsForPort(d, port)
		if events["port_cleanup_failed"] != 1 || events["port_cleanup_declined"] != 0 {
			t.Errorf("an undeclined launch must attempt the cleanup; events = %v", events)
		}
		if _, ok := d.portKillDeclined.Load(processID); ok {
			t.Error("a start that did not decline must clear a stale decline")
		}
	})
}

// The reported path end to end: port-conflict "skip" and AUTOSTART CONTINUE
// both decline the kill, and the decline must survive into StartScript.
func TestAutostart_DecliningPortCleanupKillsNothing(t *testing.T) {
	// No t.Parallel(): starts real processes.
	command, args := stayAliveExec()
	writeConfig := func(t *testing.T, dir, policy string, port int) {
		t.Helper()
		kdl := fmt.Sprintf("project {\n    port-conflict %q\n}\nscripts {\n    server {\n        %s\n        autostart true\n        ports %d\n    }\n}\n",
			policy, kdlExec(command, args), port)
		if err := os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(kdl), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("policy skip", func(t *testing.T) {
		d, _, tmpDir := newHubProcTestDaemon(t)
		port := heldPort(t)
		writeConfig(t, tmpDir, "skip", port)

		result := d.RunAutostart(ctx, tmpDir)
		if len(result.Scripts) != 1 {
			t.Fatalf("script should start despite the conflict: %+v", result)
		}
		events := startupEventsForPort(d, port)
		if events["port_conflict_skipped"] == 0 || events["port_cleanup_declined"] != 1 {
			t.Errorf("skip must be reported and carried into the launch; events = %v", events)
		}
		if events["port_cleanup_failed"] != 0 || events["port_cleanup"] != 0 {
			t.Errorf("port-conflict skip still ran the port kill; events = %v", events)
		}
	})

	t.Run("prompt then continue", func(t *testing.T) {
		d, client, tmpDir := newHubProcTestDaemon(t)
		port := heldPort(t)
		writeConfig(t, tmpDir, "prompt", port)

		if result := d.RunAutostart(ctx, tmpDir); len(result.PortConflicts) != 1 || len(result.Scripts) != 0 {
			t.Fatalf("prompt must pause on the conflict: %+v", result)
		}
		if _, err := client.AutostartContinue(tmpDir); err != nil {
			t.Fatalf("AUTOSTART CONTINUE: %v", err)
		}
		events := startupEventsForPort(d, port)
		if events["port_cleanup_declined"] != 1 {
			t.Errorf("CONTINUE must carry the decline into the launch; events = %v", events)
		}
		if events["port_cleanup_failed"] != 0 || events["port_cleanup"] != 0 {
			t.Errorf("AUTOSTART CONTINUE still ran the port kill; events = %v", events)
		}
	})
}

// A process relaunched by EADDRINUSE recovery must stay wired to the output
// pipeline: script history, the output stream, and alert classification all
// hang off the callback the first attempt was started with.
func TestStartScript_EADDRINUSERetryKeepsOutputCallback(t *testing.T) {
	// No t.Parallel(): starts real processes.
	d, _, tmpDir := newHubProcTestDaemon(t)
	marker := filepath.Join(tmpDir, "first-attempt-done")

	// First run reports EADDRINUSE and exits; the retry prints a line and stays up.
	var cfg config.ScriptConfig
	if os.PathSeparator == '\\' {
		// No quotes around the path: Go escapes them on the way to cmd.exe and
		// cmd does not unescape. shortTempDir paths carry no spaces.
		cfg.Run = fmt.Sprintf(`if exist %s (echo retry-output-line & ping -n %d 127.0.0.1 >nul) else (echo.>%s & echo Error: listen EADDRINUSE: address already in use :::1 1>&2 & exit 1)`,
			marker, stayAliveSeconds+1, marker)
	} else {
		cfg.Run = fmt.Sprintf(`if [ -e '%s' ]; then echo retry-output-line; sleep %d; else : > '%s'; echo 'Error: listen EADDRINUSE: address already in use :::1' >&2; exit 1; fi`,
			marker, stayAliveSeconds, marker)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projectPath := normalizePath(tmpDir)
	if err := d.StartScriptExplicit(ctx, "web", &cfg, projectPath, nil); err != nil {
		t.Fatalf("StartScriptExplicit: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the first attempt never ran, so no retry happened: %v", err)
	}
	entry, ok := d.scriptRegistry.Get("web", projectPath)
	if !ok {
		t.Fatal("script entry missing")
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range entry.OutputLines() {
			if strings.TrimSpace(line) == "retry-output-line" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("output of the retried process never reached script history; got %q", entry.OutputLines())
}
