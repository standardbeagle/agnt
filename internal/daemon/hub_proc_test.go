package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/go-cli-server/script"
)

// newHubProcTestDaemon spins up a daemon + client over a short unix socket.
// Shared across T4 PROC RUN / StartScriptExplicit tests. Mirrors
// newHubProxyTestDaemon in hub_proxy_test.go but without the HTTP backend
// (PROC RUN tests exercise script registry + process manager, not proxies).
func newHubProcTestDaemon(t *testing.T) (*Daemon, *Client, string) {
	t.Helper()
	tmpDir := shortTempDir(t)
	sockPath := shortSockPath(t)

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := daemon.Start(); err != nil {
		t.Fatalf("daemon.Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	})

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return daemon, client, tmpDir
}

// longRunningCmd returns a shell command string that stays alive long
// enough for SCRIPT LIST / ProcessManager assertions to race past the
// start barrier. Matches the pattern in autostart_async_test.go.
func longRunningCmd() string {
	return "sleep 60"
}

// TestStartScriptExplicit_RegistersAndStarts is the unit-level acceptance
// test for the extracted daemon-layer entrypoint. Drives a direct call (no
// hub / client round-trip) and verifies the three invariants that
// autostartScript used to enforce inline:
//
//  1. scriptConfigs cache populated under makeProcessID(projectPath, name)
//  2. scriptRegistry holds a registered entry for (name, projectPath)
//  3. ProcessManager reports a running process for the process ID
//
// If StartScriptExplicit ever drifts from autostartScript's contract
// (e.g. forgets the scriptConfigs.Store), this test fires before the
// hub-level one does.
func TestStartScriptExplicit_RegistersAndStarts(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	name := "explicit-api"
	scriptCfg := &config.ScriptConfig{
		Run: longRunningCmd(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := daemon.StartScriptExplicit(ctx, name, scriptCfg, tmpDir, nil); err != nil {
		t.Fatalf("StartScriptExplicit: %v", err)
	}
	t.Cleanup(func() {
		processID := makeProcessID(tmpDir, name)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = daemon.hub.ProcessManager().Stop(stopCtx, processID)
	})

	processID := makeProcessID(tmpDir, name)

	// 1. scriptConfigs cache.
	if _, ok := daemon.scriptConfigs.Load(processID); !ok {
		t.Errorf("scriptConfigs missing entry for %q", processID)
	}

	// 2. scriptRegistry entry.
	entry, ok := daemon.scriptRegistry.Get(name, tmpDir)
	if !ok {
		t.Fatalf("scriptRegistry missing entry for (%q, %q)", name, tmpDir)
	}
	if entry.Name != name {
		t.Errorf("entry.Name: got %q, want %q", entry.Name, name)
	}
	if entry.ProcessID != processID {
		t.Errorf("entry.ProcessID: got %q, want %q", entry.ProcessID, processID)
	}

	// 3. ProcessManager reports the process. State should be Starting or
	// Running (the process is fresh; starting may still be in-flight on
	// slow CI). A Failed here means StartScript returned OK but then
	// something tore the process down — regression worth catching.
	proc, err := daemon.hub.ProcessManager().Get(processID)
	if err != nil {
		t.Fatalf("ProcessManager.Get(%q): %v", processID, err)
	}
	state := proc.State().String()
	if state == "failed" || state == "stopped" {
		t.Errorf("process state: got %q, want starting/running", state)
	}
}

// TestStartScriptExplicit_SkipsIfAlreadyRunning pins the idempotent
// fast path — a second call with the same name is a no-op, returns nil,
// and does not churn the scriptRegistry entry. autostartScript relied
// on this for the "session B connected while session A's scripts are
// still alive" case; PROC RUN needs it too so AI agents can safely
// re-issue the command.
func TestStartScriptExplicit_SkipsIfAlreadyRunning(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	name := "idempotent"
	scriptCfg := &config.ScriptConfig{Run: longRunningCmd()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := daemon.StartScriptExplicit(ctx, name, scriptCfg, tmpDir, nil); err != nil {
		t.Fatalf("first StartScriptExplicit: %v", err)
	}
	t.Cleanup(func() {
		processID := makeProcessID(tmpDir, name)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = daemon.hub.ProcessManager().Stop(stopCtx, processID)
	})

	entry, ok := daemon.scriptRegistry.Get(name, tmpDir)
	if !ok {
		t.Fatalf("expected script entry after first start")
	}
	firstStartCount := entry.StartCount()

	// Wait until the entry reports StateStarting or StateRunning so the
	// second call hits the skip branch (if we race ahead the entry is
	// still Idle, the test accidentally re-runs startScriptWithRetry).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := entry.State(); s == script.StateStarting || s == script.StateRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s := entry.State(); s != script.StateStarting && s != script.StateRunning {
		t.Fatalf("entry state never reached starting/running: got %s", s)
	}

	// Second call is a no-op — returns nil, does not re-register.
	if err := daemon.StartScriptExplicit(ctx, name, scriptCfg, tmpDir, nil); err != nil {
		t.Fatalf("second StartScriptExplicit: %v", err)
	}

	if got := entry.StartCount(); got != firstStartCount {
		t.Errorf("StartCount after second call: got %d, want %d (idempotent)", got, firstStartCount)
	}
}

// TestHubHandleProcRun_CreatesScriptRegistryEntry exercises the
// MCP-tool-equivalent path: PROC RUN protocol command via the real
// client round-trip. Verifies the end-to-end invariant the task
// description cares about: SCRIPT LIST for the project returns the
// newly-run process as a process-kind row.
//
// Subtests share one daemon + one client; each uses a unique `name`
// so script registry keys stay disjoint.
func TestHubHandleProcRun(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	daemon, client, tmpDir := newHubProcTestDaemon(t)

	// CreatesProcessKindRow: SCRIPT LIST merges process- and proxy-kind
	// entries. A PROC-RUN-started process must appear with kind=process.
	t.Run("CreatesProcessKindRow", func(t *testing.T) {
		name := "mcp-run"
		resp, err := client.ProcRun(name, ProcRunConfig{
			Run:         longRunningCmd(),
			ProjectPath: tmpDir,
		})
		if err != nil {
			t.Fatalf("ProcRun: %v", err)
		}
		t.Cleanup(func() {
			processID := makeProcessID(tmpDir, name)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer stopCancel()
			_ = daemon.hub.ProcessManager().Stop(stopCtx, processID)
		})

		// Response shape: process_id, project_path, name must be present.
		if got, _ := resp["name"].(string); got != name {
			t.Errorf("resp.name: got %v, want %q", resp["name"], name)
		}
		processID := makeProcessID(tmpDir, name)
		if got, _ := resp["process_id"].(string); got != processID {
			t.Errorf("resp.process_id: got %v, want %q", resp["process_id"], processID)
		}

		// SCRIPT LIST contract: the process appears as a process-kind row.
		summaries := daemon.buildScriptListSummaries(tmpDir)
		var row map[string]interface{}
		for _, s := range summaries {
			if s["name"] == name {
				row = s
				break
			}
		}
		if row == nil {
			t.Fatalf("SCRIPT LIST missing row for %q; rows=%+v", name, summaries)
		}
		if row["kind"] != string(ScriptKindProcess) {
			t.Errorf("row.kind: got %v, want %q", row["kind"], ScriptKindProcess)
		}
		if row["process_id"] != processID {
			t.Errorf("row.process_id: got %v, want %q", row["process_id"], processID)
		}
	})

	// MissingName: empty name arg → invalid args error; no script entry
	// side effect.
	t.Run("MissingName", func(t *testing.T) {
		// The Request.JSON() path returns an error when the daemon writes
		// ErrMissingParam. A raw empty string arg passes through CommandArgs
		// untouched, so the daemon-side check fires. Drive the client's
		// lower-level Request to send an empty name.
		_, err := client.conn.Request("PROC", "RUN", "").
			WithJSON(ProcRunConfig{Run: longRunningCmd(), ProjectPath: tmpDir}).
			JSON()
		if err == nil {
			t.Fatalf("expected error for empty name, got nil")
		}
	})

	// MissingCommand: payload without `run` or `command` → missing param.
	t.Run("MissingCommand", func(t *testing.T) {
		_, err := client.ProcRun("no-cmd", ProcRunConfig{
			ProjectPath: tmpDir,
		})
		if err == nil {
			t.Fatalf("expected error for payload without run/command, got nil")
		}
	})

	// MissingProjectPath: unbound session and no ProjectPath override →
	// missing param error.
	t.Run("MissingProjectPath", func(t *testing.T) {
		_, err := client.ProcRun("no-path", ProcRunConfig{
			Run: longRunningCmd(),
		})
		if err == nil {
			t.Fatalf("expected error for missing project_path, got nil")
		}
	})
}
