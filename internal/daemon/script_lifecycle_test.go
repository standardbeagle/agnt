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
	"github.com/stretchr/testify/require"
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

		var scripts []*script.Entry
		require.Eventually(t, func() bool {
			scripts = d.ScriptRegistry().List(normalizePath(tmpDir))
			if len(scripts) != 3 {
				return false
			}
			for _, s := range scripts {
				if s.State() == script.StateIdle || s.State() == script.StateStarting {
					return false
				}
			}
			return true
		}, 10*time.Second, 20*time.Millisecond, "scripts did not all reach non-idle/non-starting")

		require.Len(t, scripts, 3, "exactly 3 scripts should be registered")
		for _, s := range scripts {
			state := s.State()
			require.NotEqual(t, script.StateIdle, state, "script %s should not be idle after autostart", s.Name)
			require.NotEmpty(t, s.Name, "script entry must have a non-empty name")
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

		var entry *script.Entry
		require.Eventually(t, func() bool {
			e, ok := d.ScriptRegistry().Get("quick", normalizePath(tmpDir))
			if !ok {
				return false
			}
			s := e.State()
			if s != script.StateStopped && s != script.StateFailed {
				return false
			}
			for _, line := range e.OutputLines() {
				if line == "goodbye" {
					entry = e
					return true
				}
			}
			return false
		}, 10*time.Second, 20*time.Millisecond, "process did not exit with expected output within 10s")

		state := entry.State()
		require.True(t, state == script.StateStopped || state == script.StateFailed,
			"expected stopped or failed, got %s", state)
		require.Equal(t, "quick", entry.Name, "entry name must match script name")
		require.GreaterOrEqual(t, entry.StartCount(), int64(1), "start count should be at least 1")
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

		var entry *script.Entry
		require.Eventually(t, func() bool {
			e, ok := d.ScriptRegistry().Get("broken", normalizePath(tmpDir))
			if !ok {
				return false
			}
			entry = e
			return e.State() == script.StateFailed && e.FailCount() >= 1
		}, 10*time.Second, 20*time.Millisecond, "script did not reach StateFailed with FailCount>=1 within 10s")

		require.Equal(t, script.StateFailed, entry.State(), "expected StateFailed after non-zero exit")
		require.GreaterOrEqual(t, entry.FailCount(), int64(1), "FailCount should be at least 1")
		require.Equal(t, "broken", entry.Name, "entry name must match script name")
	})

	// StandaloneRunCreatesScript: starting a process directly via
	// StartScript (no .agnt.kdl autostart) should still populate the
	// registry so admin surfaces see it.
	t.Run("StandaloneRunCreatesScript", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Pick a long-running command that exists on the host. sh isn't
		// on PATH on Windows Go runners; use ping -n 30 to keep the
		// process alive ~29s. Avoid cmd.exe + shell redirect tricks —
		// exec.Command doesn't go through a shell, so '> nul' would be
		// a literal arg and the whole thing would fail with exit 1.
		cmd, args := "sh", []string{"-c", "sleep 30"}
		if runtime.GOOS == "windows" {
			cmd, args = "ping", []string{"-n", "30", "127.0.0.1"}
		}

		// Use a stable WorkingDir (os.TempDir root) instead of the
		// t.TempDir() that will be cleaned up at test end. On Windows,
		// the spawned process holds its cwd open until it exits, and
		// t.Cleanup's RemoveAll on a cwd-held dir fails with 'file in
		// use by another process'. Keeping ProjectPath = tmpDir keeps
		// registry lookups keyed by the test's unique projectPath.
		workingDir := os.TempDir()
		processID := makeProcessID(normalizePath(tmpDir), "manual")
		_, err := d.StartScript(context.Background(), StartScriptConfig{
			ProcessID:   processID,
			ProjectPath: tmpDir,
			WorkingDir:  workingDir,
			Command:     cmd,
			Args:        args,
		})
		if err != nil {
			t.Fatalf("StartScript failed: %v", err)
		}
		// Stop the process before the test exits so the daemon's
		// deferred Stop doesn't collide with subtest cleanup.
		t.Cleanup(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = d.ProcessManager().Stop(stopCtx, processID)
		})

		var entry *script.Entry
		require.Eventually(t, func() bool {
			e, ok := d.ScriptRegistry().Get("manual", normalizePath(tmpDir))
			if !ok {
				return false
			}
			entry = e
			return true
		}, 5*time.Second, 10*time.Millisecond, "StartScript did not create registry entry within 5s")

		require.Equal(t, "manual", entry.Name, "entry name must be 'manual'")
		require.NotEmpty(t, entry.ProcessID, "entry must have a non-empty ProcessID")
		require.NotEqual(t, script.StateIdle, entry.State(), "standalone started script should not be idle")
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

		// Wait for the ephemeral script to appear in the registry and reach a terminal state.
		require.Eventually(t, func() bool {
			e, ok := d.ScriptRegistry().Get("ephemeral", normalizePath(tmpDir))
			if !ok {
				return false
			}
			s := e.State()
			return s == script.StateStopped || s == script.StateFailed
		}, 10*time.Second, 20*time.Millisecond, "ephemeral script did not reach terminal state within 10s")

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
