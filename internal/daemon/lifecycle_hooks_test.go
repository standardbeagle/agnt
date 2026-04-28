//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/require"
)

// waitForHookProcessState polls until the script named scriptName reaches
// wantState or timeout elapses. It scans the process list by name suffix
// so callers can pass the plain script name without computing the full
// project-scoped process ID.
func waitForHookProcessState(t *testing.T, d *Daemon, scriptName string, wantState goprocess.ProcessState, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, p := range d.hub.ProcessManager().List() {
			if stripProcessPrefix(p.ID) == scriptName {
				return p.State() == wantState
			}
		}
		return false
	}, timeout, 100*time.Millisecond, "process %s did not reach state %s", scriptName, wantState)
}

func TestLifecycleHooks_OnStart(t *testing.T) {
	// No t.Parallel(): starts real processes; PID-reuse kills them under high concurrency.
	if runtime.GOOS == "windows" {
		t.Skip("shell differences covered by TestRunLifecycleHook_SetsEnvVars")
	}

	dir := t.TempDir()
	flagFile := filepath.Join(dir, "on-start-fired")

	writeConfig(t, dir, `scripts {
    svc {
        run "sleep 60"
        autostart true
        hooks {
            on-start "touch `+flagFile+`"
        }
    }
}`)

	d := newDaemon(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.RunAutostart(ctx, dir)

	waitForHookProcessState(t, d, "svc", goprocess.StateRunning, 10*time.Second)

	require.Eventually(t, func() bool {
		_, err := os.Stat(flagFile)
		return err == nil
	}, 3*time.Second, 50*time.Millisecond, "on-start flag file not created")
}

func TestLifecycleHooks_OnStop(t *testing.T) {
	// No t.Parallel(): starts real processes.
	if runtime.GOOS == "windows" {
		t.Skip("covered by hook_runner_test.go")
	}

	dir := t.TempDir()
	flagFile := filepath.Join(dir, "on-stop-fired")

	writeConfig(t, dir, `scripts {
    svc {
        run "sleep 60"
        autostart true
        hooks {
            on-stop "touch `+flagFile+`"
        }
    }
}`)

	d := newDaemon(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.RunAutostart(ctx, dir)

	waitForHookProcessState(t, d, "svc", goprocess.StateRunning, 10*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = d.hub.ProcessManager().Stop(stopCtx, makeProcessID(dir, "svc"))

	require.Eventually(t, func() bool {
		_, err := os.Stat(flagFile)
		return err == nil
	}, 8*time.Second, 50*time.Millisecond, "on-stop flag file not created")
}

func TestLifecycleHooks_EnvVarsInjected(t *testing.T) {
	// No t.Parallel(): starts real processes.
	if runtime.GOOS == "windows" {
		t.Skip("covered by hook_runner_test.go")
	}

	dir := t.TempDir()
	envFile := filepath.Join(dir, "hook-env.txt")

	writeConfig(t, dir, `scripts {
    svc {
        run "sleep 60"
        autostart true
        hooks {
            on-start "printf '%s:%s' \"$AGNT_EVENT\" \"$AGNT_SCRIPT_ID\" > `+envFile+`"
        }
    }
}`)

	d := newDaemon(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.RunAutostart(ctx, dir)

	waitForHookProcessState(t, d, "svc", goprocess.StateRunning, 10*time.Second)

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(envFile)
		if err != nil {
			return false
		}
		return strings.Contains(strings.TrimSpace(string(data)), "start:svc")
	}, 3*time.Second, 50*time.Millisecond, "env vars not injected into on-start hook")
}

func TestLifecycleHooks_OnCrash(t *testing.T) {
	// No t.Parallel(): starts real processes.
	if runtime.GOOS == "windows" {
		t.Skip("covered by hook_runner_test.go")
	}

	dir := t.TempDir()
	flagFile := filepath.Join(dir, "on-crash-fired")

	// Process exits non-zero (crash); no auto-restart so it only fires once.
	writeConfig(t, dir, `scripts {
    svc {
        run "exit 42"
        autostart true
        hooks {
            on-crash "touch `+flagFile+`"
        }
    }
}`)

	d := newDaemon(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.RunAutostart(ctx, dir)

	require.Eventually(t, func() bool {
		_, err := os.Stat(flagFile)
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "on-crash hook did not fire after non-zero exit")
}

func TestLifecycleHooks_OnRestart(t *testing.T) {
	// No t.Parallel(): starts real processes.
	if runtime.GOOS == "windows" {
		t.Skip("covered by hook_runner_test.go")
	}

	dir := t.TempDir()
	flagFile := filepath.Join(dir, "on-restart-fired")

	// Process exits immediately triggering auto-restart.
	writeConfig(t, dir, `scripts {
    svc {
        run "exit 1"
        autostart true
        auto-restart true
        hooks {
            on-restart "touch `+flagFile+`"
        }
    }
}`)

	d := newDaemon(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d.RunAutostart(ctx, dir)

	require.Eventually(t, func() bool {
		_, err := os.Stat(flagFile)
		return err == nil
	}, 15*time.Second, 100*time.Millisecond, "on-restart hook did not fire")
}
