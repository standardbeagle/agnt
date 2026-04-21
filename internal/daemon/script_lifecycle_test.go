package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/go-cli-server/script"
)

// TestScriptLifecycle covers the full script lifecycle in one shared daemon
// to avoid paying the daemon boot cost 8 times. Each subtest uses its own
// tmpDir → the registry keys stay disjoint across subtests, so they don't
// interfere. Each subtest still makes its own assertions end-to-end.
//
// The only shared state is the daemon itself; shutdown happens via the
// Cleanup registered by newBootedDaemon at test-end.
func TestScriptLifecycle(t *testing.T) {
	d, _ := newBootedDaemon(t)

	// ConfigCreatesIdleScripts: loading .agnt.kdl should surface every
	// declared script in the registry after RunAutostart, regardless of
	// autostart flag, and none should remain StateIdle once autostart has
	// fired for them.
	t.Run("ConfigCreatesIdleScripts", func(t *testing.T) {
		tmpDir := t.TempDir()

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
		if err := os.MkdirAll(filepath.Join(tmpDir, "lib"), 0o755); err != nil {
			t.Fatalf("Failed to create lib dir: %v", err)
		}

		d.RunAutostart(context.Background(), tmpDir)
		time.Sleep(1 * time.Second)

		scripts := d.ScriptRegistry().List(normalizePath(tmpDir))
		if len(scripts) != 3 {
			names := make([]string, len(scripts))
			for i, s := range scripts {
				names[i] = s.Name + ":" + s.State().String()
			}
			t.Fatalf("Expected 3 scripts, got %d: %v", len(scripts), names)
		}

		for _, s := range scripts {
			state := s.State()
			if state == script.StateIdle {
				t.Errorf("Script %s should not be idle after autostart (state=%s)", s.Name, state)
			}
		}
	})

	// ProcessExitPreservesScript: a process that runs briefly and exits
	// normally leaves the ScriptEntry behind with a non-running state and
	// its output still accessible.
	t.Run("ProcessExitPreservesScript", func(t *testing.T) {
		tmpDir := t.TempDir()

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

		d.RunAutostart(context.Background(), tmpDir)
		time.Sleep(2 * time.Second)

		entry, ok := d.ScriptRegistry().Get("quick", normalizePath(tmpDir))
		if !ok {
			t.Fatal("ScriptEntry should persist after process exit")
		}

		state := entry.State()
		if state != script.StateStopped && state != script.StateFailed {
			t.Errorf("Expected stopped or failed after exit, got %s", state)
		}

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
	})

	// FailedScriptHasError: a script that exits non-zero should reach
	// StateFailed and bump FailCount at least once.
	t.Run("FailedScriptHasError", func(t *testing.T) {
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

		d.RunAutostart(context.Background(), tmpDir)
		time.Sleep(2 * time.Second)

		entry, ok := d.ScriptRegistry().Get("broken", normalizePath(tmpDir))
		if !ok {
			t.Fatal("ScriptEntry should persist after failure")
		}

		if state := entry.State(); state != script.StateFailed {
			t.Errorf("Expected StateFailed, got %s", state)
		}

		if entry.FailCount() < 1 {
			t.Error("FailCount should be at least 1")
		}
	})

	// StandaloneRunCreatesScript: starting a process directly via
	// StartScript (no .agnt.kdl autostart) should still populate the
	// registry so admin surfaces see it.
	t.Run("StandaloneRunCreatesScript", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Pick a shell that exists on the host. Windows Go runners don't
		// have sh on PATH; use cmd.exe + a long-running ping equivalent.
		// (timeout.exe /T N > nul keeps the process alive for ~N seconds
		// without writing output.)
		cmd, args := "sh", []string{"-c", "sleep 30"}
		if runtime.GOOS == "windows" {
			cmd, args = "cmd", []string{"/c", "timeout", "/t", "30", "/nobreak", ">", "nul"}
		}

		_, err := d.StartScript(context.Background(), StartScriptConfig{
			ProcessID:   makeProcessID(normalizePath(tmpDir), "manual"),
			ProjectPath: tmpDir,
			WorkingDir:  tmpDir,
			Command:     cmd,
			Args:        args,
		})
		if err != nil {
			t.Fatalf("StartScript failed: %v", err)
		}

		time.Sleep(500 * time.Millisecond)

		entry, ok := d.ScriptRegistry().Get("manual", normalizePath(tmpDir))
		if !ok {
			t.Fatal("StartScript should auto-create a ScriptEntry for standalone processes")
		}

		if entry.Name != "manual" {
			t.Errorf("Expected name 'manual', got %q", entry.Name)
		}
	})

	// ScriptListViaProtocol: registry survives the underlying process
	// exiting. The ProcessManager may or may not still hold a record,
	// but the script is still listable.
	t.Run("ScriptListViaProtocol", func(t *testing.T) {
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

		d.RunAutostart(context.Background(), tmpDir)
		time.Sleep(2 * time.Second)

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
	})

	// NonAutostartScriptsRegistered: a script without autostart=true is
	// still registered after RunAutostart, and stays in StateIdle.
	t.Run("NonAutostartScriptsRegistered", func(t *testing.T) {
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

		d.RunAutostart(context.Background(), tmpDir)

		entry, ok := d.ScriptRegistry().Get("manual-only", normalizePath(tmpDir))
		if !ok {
			t.Fatal("Non-autostart scripts should be registered in ScriptRegistry as idle")
		}

		if entry.State() != script.StateIdle {
			t.Errorf("Expected StateIdle for non-autostart script, got %s", entry.State())
		}
	})

	// ConfigPortsUsedForCleanup: getExpectedPortsForScript surfaces the
	// port declared in .agnt.kdl, so startup-time port cleanup can target
	// it before launch.
	t.Run("ConfigPortsUsedForCleanup", func(t *testing.T) {
		tmpDir := t.TempDir()

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
	})

	// MultiplePortsFromConfig: getExpectedPortsForScript returns every
	// port from a multi-port declaration.
	t.Run("MultiplePortsFromConfig", func(t *testing.T) {
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
	})
}
