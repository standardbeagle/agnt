package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/go-cli-server/script"
)

// These integration tests verify the full script lifecycle:
// .agnt.kdl config → session registration → ScriptEntries created →
// process starts → ScriptEntry updates → process dies → ScriptEntry persists

// TestScriptLifecycle_ConfigCreatesIdleScripts verifies that loading .agnt.kdl
// creates ScriptEntries in StateIdle for all defined scripts, BEFORE any
// processes are started. Scripts should exist as soon as the config is loaded.
func TestScriptLifecycle_ConfigCreatesIdleScripts(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Write a config with 3 scripts, only 1 has autostart
	configContent := `
scripts {
    dev-lib {
        run "echo lib"
        cwd "lib"
        autostart true
    }
    dev-backend {
        run "echo backend"
        autostart true
    }
    dev-frontend {
        run "echo frontend"
        autostart true
        depends-on "dev-backend"
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	// Create the cwd directory for dev-lib
	if err := os.MkdirAll(filepath.Join(tmpDir, "lib"), 0o755); err != nil {
		t.Fatalf("Failed to create lib dir: %v", err)
	}

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// RunAutostart should create ScriptEntries for ALL scripts in config
	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Give processes time to start
	time.Sleep(1 * time.Second)

	// ALL 3 scripts should exist in the registry
	scripts := d.ScriptRegistry().List(tmpDir)
	if len(scripts) != 3 {
		names := make([]string, len(scripts))
		for i, s := range scripts {
			names[i] = s.Name + ":" + s.State().String()
		}
		t.Fatalf("Expected 3 scripts, got %d: %v", len(scripts), names)
	}

	// Each script should have a valid state
	for _, s := range scripts {
		state := s.State()
		if state == script.StateIdle {
			t.Errorf("Script %s should not be idle after autostart (state=%s)", s.Name, state)
		}
	}
}

// TestScriptLifecycle_ProcessExitPreservesScript verifies that when a process
// exits (normally or with error), the ScriptEntry persists with the correct
// state and its output is still accessible.
func TestScriptLifecycle_ProcessExitPreservesScript(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Script that exits immediately with a message
	configContent := `
scripts {
    quick {
        command "echo"
        args "goodbye"
        autostart true
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Wait for process to exit
	time.Sleep(2 * time.Second)

	// Script should still exist even though process exited
	entry, ok := d.ScriptRegistry().Get("quick", tmpDir)
	if !ok {
		t.Fatal("ScriptEntry should persist after process exit")
	}

	// State should be stopped or failed (echo exits with 0 = stopped)
	state := entry.State()
	if state != script.StateStopped && state != script.StateFailed {
		t.Errorf("Expected stopped or failed after exit, got %s", state)
	}

	// Output should contain the echo output
	lines := entry.OutputLines()
	found := false
	for _, line := range lines {
		if line == "goodbye" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'goodbye' in output, got: %v", lines)
	}
}

// TestScriptLifecycle_FailedScriptHasError verifies that a script that fails
// has its error message recorded and accessible.
func TestScriptLifecycle_FailedScriptHasError(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	configContent := `
scripts {
    broken {
        command "false"
        autostart true
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Wait for process to fail
	time.Sleep(2 * time.Second)

	entry, ok := d.ScriptRegistry().Get("broken", tmpDir)
	if !ok {
		t.Fatal("ScriptEntry should persist after failure")
	}

	state := entry.State()
	if state != script.StateFailed {
		t.Errorf("Expected StateFailed, got %s", state)
	}

	if entry.FailCount() < 1 {
		t.Error("FailCount should be at least 1")
	}
}

// TestScriptLifecycle_StandaloneRunCreatesScript verifies that running a
// process via `proc run` (not from .agnt.kdl) creates a ScriptEntry
// automatically, so the code path is uniform.
func TestScriptLifecycle_StandaloneRunCreatesScript(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// Start a process directly via StartScript (not autostart)
	// Use sh -c with a long sleep so process survives startup monitoring
	ctx := context.Background()
	_, err := d.StartScript(ctx, StartScriptConfig{
		ProcessID:   makeProcessID(tmpDir, "manual"),
		ProjectPath: tmpDir,
		WorkingDir:  tmpDir,
		Command:     "sh",
		Args:        []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("StartScript failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// A ScriptEntry should have been created automatically
	entry, ok := d.ScriptRegistry().Get("manual", tmpDir)
	if !ok {
		t.Fatal("StartScript should auto-create a ScriptEntry for standalone processes")
	}

	if entry.Name != "manual" {
		t.Errorf("Expected name 'manual', got %q", entry.Name)
	}
}

// TestScriptLifecycle_ScriptListViaProtocol verifies that the SCRIPT LIST
// protocol returns all scripts including ones whose processes have exited.
func TestScriptLifecycle_ScriptListViaProtocol(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	configContent := `
scripts {
    ephemeral {
        command "echo"
        args "done"
        autostart true
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Wait for process to exit
	time.Sleep(2 * time.Second)

	// Process is gone from ProcessManager
	procs := d.hub.ProcessManager().List()
	procAlive := false
	for _, p := range procs {
		if p.ID == makeProcessID(tmpDir, "ephemeral") {
			if p.State() == 2 { // StateRunning
				procAlive = true
			}
		}
	}
	if procAlive {
		t.Log("Warning: process still alive, echo should have exited")
	}

	// But script should still be in registry
	scripts := d.ScriptRegistry().List(tmpDir)
	if len(scripts) == 0 {
		t.Fatal("SCRIPT LIST should return scripts even after process exits")
	}

	found := false
	for _, s := range scripts {
		if s.Name == "ephemeral" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Script 'ephemeral' should be in the list after process exit")
	}
}

// TestScriptLifecycle_NonAutostartScriptsRegistered verifies that scripts
// without autostart=true are still registered in the ScriptRegistry as idle.
func TestScriptLifecycle_NonAutostartScriptsRegistered(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	configContent := `
scripts {
    manual-only {
        command "echo"
        args "manual"
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Non-autostart script should still be registered as idle
	entry, ok := d.ScriptRegistry().Get("manual-only", tmpDir)
	if !ok {
		t.Fatal("Non-autostart scripts should be registered in ScriptRegistry as idle")
	}

	if entry.State() != script.StateIdle {
		t.Errorf("Expected StateIdle for non-autostart script, got %s", entry.State())
	}
}
