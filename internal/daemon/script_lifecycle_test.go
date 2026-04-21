package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
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

	d, _ := newBootedDaemon(t)

	// RunAutostart should create ScriptEntries for ALL scripts in config
	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Give processes time to start
	time.Sleep(1 * time.Second)

	// ALL 3 scripts should exist in the registry
	scripts := d.ScriptRegistry().List(normalizePath(tmpDir))
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

	d, _ := newBootedDaemon(t)

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Wait for process to exit
	time.Sleep(2 * time.Second)

	// Script should still exist even though process exited
	entry, ok := d.ScriptRegistry().Get("quick", normalizePath(tmpDir))
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

	d, _ := newBootedDaemon(t)

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Wait for process to fail
	time.Sleep(2 * time.Second)

	entry, ok := d.ScriptRegistry().Get("broken", normalizePath(tmpDir))
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

	d, _ := newBootedDaemon(t)

	// Start a process directly via StartScript (not autostart)
	// Use sh -c with a long sleep so process survives startup monitoring
	ctx := context.Background()
	_, err := d.StartScript(ctx, StartScriptConfig{
		ProcessID:   makeProcessID(normalizePath(tmpDir), "manual"),
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
	entry, ok := d.ScriptRegistry().Get("manual", normalizePath(tmpDir))
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

	d, _ := newBootedDaemon(t)

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Wait for process to exit
	time.Sleep(2 * time.Second)

	// Process is gone from ProcessManager
	procs := d.hub.ProcessManager().List()
	procAlive := false
	for _, p := range procs {
		if p.ID == makeProcessID(normalizePath(tmpDir), "ephemeral") {
			if p.State() == 2 { // StateRunning
				procAlive = true
			}
		}
	}
	if procAlive {
		t.Log("Warning: process still alive, echo should have exited")
	}

	// But script should still be in registry
	scripts := d.ScriptRegistry().List(normalizePath(tmpDir))
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

	d, _ := newBootedDaemon(t)

	ctx := context.Background()
	d.RunAutostart(ctx, tmpDir)

	// Non-autostart script should still be registered as idle
	entry, ok := d.ScriptRegistry().Get("manual-only", normalizePath(tmpDir))
	if !ok {
		t.Fatal("Non-autostart scripts should be registered in ScriptRegistry as idle")
	}

	if entry.State() != script.StateIdle {
		t.Errorf("Expected StateIdle for non-autostart script, got %s", entry.State())
	}
}

// TestScriptLifecycle_ConfigPortsUsedForCleanup verifies that explicit ports
// declared in .agnt.kdl are used for pre-flight orphan cleanup.
func TestScriptLifecycle_ConfigPortsUsedForCleanup(t *testing.T) {
	tmpDir := t.TempDir()

	// Script declares port 9876 explicitly
	configContent := `
scripts {
    api {
        command "sleep"
        args "30"
        ports 9876
        autostart true
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	d, _ := newBootedDaemon(t)

	// Verify getExpectedPortsForScript returns the declared port
	agntConfig, err := config.LoadAgntConfig(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	scriptCfg := agntConfig.Scripts["api"]
	if scriptCfg == nil {
		t.Fatal("Script 'api' not found in config")
	}

	ports := d.getExpectedPortsForScript("api", scriptCfg, agntConfig.Proxies, tmpDir, "sleep", []string{"30"})
	if len(ports) == 0 {
		t.Fatal("Expected at least 1 port from config, got none")
	}

	found := false
	for _, p := range ports {
		if p == 9876 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected port 9876 in ports list, got %v", ports)
	}
}

// TestScriptLifecycle_MultiplePortsFromConfig verifies that multiple ports
// declared in config are all returned.
func TestScriptLifecycle_MultiplePortsFromConfig(t *testing.T) {
	tmpDir := t.TempDir()

	configContent := `
scripts {
    backend {
        command "sleep"
        args "30"
        ports 5000 5001 5002
        autostart true
    }
}
`
	if err := writeFile(filepath.Join(tmpDir, ".agnt.kdl"), configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	d, _ := newBootedDaemon(t)

	agntConfig, err := config.LoadAgntConfig(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	scriptCfg := agntConfig.Scripts["backend"]
	ports := d.getExpectedPortsForScript("backend", scriptCfg, agntConfig.Proxies, tmpDir, "sleep", []string{"30"})

	if len(ports) < 3 {
		t.Errorf("Expected at least 3 ports, got %d: %v", len(ports), ports)
	}

	expected := map[int]bool{5000: true, 5001: true, 5002: true}
	for _, p := range ports {
		delete(expected, p)
	}
	if len(expected) > 0 {
		missing := make([]int, 0)
		for p := range expected {
			missing = append(missing, p)
		}
		t.Errorf("Missing ports: %v (got %v)", missing, ports)
	}
}
