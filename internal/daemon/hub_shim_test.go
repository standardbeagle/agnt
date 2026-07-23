package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/shims"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// shimTestProject creates a temp project with the given .agnt.kdl body.
func shimTestProject(t *testing.T, kdl string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(kdl), 0o644))
	return dir
}

func shimTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return NewForTest(t, DaemonConfig{
		SocketPath:        ephemeralTestSockPath(t),
		MaxClients:        10,
		WriteTimeout:      5 * time.Second,
		OrphanScanEnabled: false,
		StatePath:         t.TempDir(),
	})
}

func TestRouteShim_UnshimmedCommandPasses(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-s1", project, "")

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "rm",
		Args:        []string{"-rf", "/tmp/x"},
	})
	assert.Equal(t, "passthrough", resp.Action)
}

func TestRouteShim_NoSessionPasses(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "npm",
		Args:        []string{"run", "dev"},
	})
	assert.Equal(t, "passthrough", resp.Action, "no registered session → fail open")
}

func TestRouteShim_DisabledPasses(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "shims {\n    enabled false\n}\n")
	activeSession(t, d.sessionRegistry, "shim-s2", project, "")

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "npm",
		Args:        []string{"run", "dev"},
	})
	assert.Equal(t, "passthrough", resp.Action)
}

func TestRouteShim_GenericCommandPasses(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-s3", project, "")

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "npm",
		Args:        []string{"install"},
	})
	assert.Equal(t, "passthrough", resp.Action)
}

func TestRouteShim_RuleIgnoreAndBlock(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, `shims {
    rules {
        no-tests {
            match "npm test"
            action "block"
        }
        skip-lint {
            match "npm run lint"
            action "ignore"
        }
    }
}
`)
	activeSession(t, d.sessionRegistry, "shim-s4", project, "")

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project, Command: "npm", Args: []string{"test"},
	})
	assert.Equal(t, "blocked", resp.Action)
	assert.Equal(t, 2, resp.ExitCode)
	assert.Contains(t, resp.Message, "no-tests")

	resp = d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project, Command: "npm", Args: []string{"run", "lint"},
	})
	assert.Equal(t, "handled", resp.Action)
	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Message, "ignored")
}

func TestRouteShim_KillUnmanagedPasses(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-s5", project, "")

	// PID 2^22 is virtually guaranteed not to be a daemon-managed process.
	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project, Command: "kill", Args: []string{"-9", "4194304"},
	})
	assert.Equal(t, "passthrough", resp.Action)
}

func TestRouteShim_PortReport(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-s6", project, "")

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project, Command: "lsof", Args: []string{"-i", ":3000"},
	})
	assert.Equal(t, "handled", resp.Action)
	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Output, "no managed processes")
	assert.Contains(t, resp.ToolHint, "cleanup_port")
}

func TestShimKillPIDParsing(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []int{1234}, shimKillPIDs([]string{"1234"}))
	assert.Equal(t, []int{1234, 5678}, shimKillPIDs([]string{"-9", "1234", "5678"}))
	assert.Equal(t, []int{1234}, shimKillPIDs([]string{"-s", "SIGKILL", "1234"}))
	assert.Equal(t, []int{1234}, shimKillPIDs([]string{"--signal", "TERM", "1234"}))
	assert.Empty(t, shimKillPIDs([]string{"-9"}))
	assert.Empty(t, shimKillPIDs([]string{"notapid"}))
}

func TestShimInterceptableSignal(t *testing.T) {
	t.Parallel()
	// Interceptable: default and explicit TERM/KILL in every flag form.
	assert.True(t, shimInterceptableSignal([]string{"1234"}))
	assert.True(t, shimInterceptableSignal([]string{"-9", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"-KILL", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"-SIGKILL", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"-TERM", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"-15", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"-s", "SIGKILL", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"--signal", "TERM", "1234"}))
	assert.True(t, shimInterceptableSignal([]string{"--signal=KILL", "1234"}))
	// Not interceptable: probes, listing, non-terminating signals, unknown flags.
	assert.False(t, shimInterceptableSignal([]string{"-0", "1234"}), "liveness probe must pass through")
	assert.False(t, shimInterceptableSignal([]string{"-l"}))
	assert.False(t, shimInterceptableSignal([]string{"-l", "9"}), "signal listing must not parse as a PID")
	assert.False(t, shimInterceptableSignal([]string{"-HUP", "1234"}))
	assert.False(t, shimInterceptableSignal([]string{"-USR1", "1234"}))
	assert.False(t, shimInterceptableSignal([]string{"-u", "bob", "node"}))
}

// TestRouteShim_KillProbePasses pins the probe-safety fix: `kill -0`,
// `kill -l`, and non-terminating signals on a MANAGED pid must pass
// through without stopping the process. Spawns a real sleep — no
// t.Parallel per repo exec-test rule.
func TestRouteShim_KillProbePasses(t *testing.T) {
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-probe", project, "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pm := d.hub.ProcessManager()
	result, err := pm.StartOrReuse(ctx, goprocess.ProcessConfig{
		ID: "probe-sleeper", ProjectPath: project, Command: "sleep", Args: []string{"30"},
	})
	require.NoError(t, err)
	managedPID := result.Process.PID()
	require.Positive(t, managedPID)

	for _, args := range [][]string{
		{"-0", strconv.Itoa(managedPID)},
		{"-l", strconv.Itoa(managedPID)},
		{"-HUP", strconv.Itoa(managedPID)},
	} {
		resp := d.routeShim(ctx, &protocol.ShimExecRequest{
			ProjectPath: project, Command: "kill", Args: args,
		})
		assert.Equal(t, "passthrough", resp.Action, "args %v must pass through", args)
		assert.True(t, result.Process.IsRunning(), "args %v must not stop the managed process", args)
	}

	// Explicit KILL is still intercepted.
	resp := d.routeShim(ctx, &protocol.ShimExecRequest{
		ProjectPath: project, Command: "kill",
		Args: []string{"-9", strconv.Itoa(managedPID)},
	})
	assert.Equal(t, "handled", resp.Action)
}

// TestShimRunOneShot_ClientCancelStopsProcess pins the registry-leak fix:
// when the request context dies mid-build, the managed process is stopped
// and removed, not left running and registered. Spawns a real sleep — no
// t.Parallel per repo exec-test rule.
func TestShimRunOneShot_ClientCancelStopsProcess(t *testing.T) {
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *protocol.ShimExecResponse, 1)
	go func() {
		done <- d.shimRunOneShot(ctx, &protocol.ShimExecRequest{
			ProjectPath: project, Command: "sleep", Args: []string{"30"},
		}, "sleep 30", "", nil, nil)
	}()

	// Wait until the managed process exists, then kill the request context.
	deadline := time.Now().Add(10 * time.Second)
	for {
		started := false
		for _, p := range d.hub.ProcessManager().List() {
			if strings.HasPrefix(p.ID, "shim-sleep-30-") {
				started = true
			}
		}
		if started {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("one-shot process never appeared in the registry")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case resp := <-done:
		assert.Equal(t, 1, resp.ExitCode)
	case <-time.After(20 * time.Second):
		t.Fatal("shimRunOneShot did not return after context cancel")
	}

	for _, p := range d.hub.ProcessManager().List() {
		assert.NotContains(t, p.ID, "shim-sleep-30", "cancelled one-shot must be deregistered")
	}
}

// TestReleaseProjectShims pins the stale-session-code leak fix: a session
// ending while others live must detach its manifest code (so it can't pin
// the entry) without removing the bin dir; the last release removes both.
// Mutates XDG_STATE_HOME and the shared manifest — no t.Parallel.
func TestReleaseProjectShims(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	s1 := activeSession(t, d.sessionRegistry, "shim-r1", project, "")
	s2 := activeSession(t, d.sessionRegistry, "shim-r2", project, "")

	binDir, err := shims.Ensure(project)
	require.NoError(t, err)
	require.NotEmpty(t, binDir)
	require.NoError(t, shims.RecordInstall(project, binDir, "shim-r1"))
	require.NoError(t, shims.RecordInstall(project, binDir, "shim-r2"))

	// First session ends while another lives: code detached, dir kept.
	d.releaseProjectShims(project, "shim-r1")
	assert.DirExists(t, binDir)
	m := shims.LoadManifest()
	require.NotNil(t, m.Projects[project])
	assert.Equal(t, []string{"shim-r2"}, m.Projects[project].Sessions,
		"ended session's code must not linger in the manifest")

	// Both sessions gone: the last release removes dir and entry.
	d.sessionRegistry.UnregisterExact("shim-r1", s1)
	d.sessionRegistry.UnregisterExact("shim-r2", s2)
	d.releaseProjectShims(project, "shim-r2")
	assert.NoDirExists(t, binDir)
	assert.Nil(t, shims.LoadManifest().Projects[project])
}

func TestShimSlug(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "npm-run-dev", shimSlug("npm run dev"))
	assert.Equal(t, "go-build", shimSlug("go build ./..."))
	assert.Equal(t, "vite", shimSlug("vite"))
}

// TestRouteShim_OneShotRunsManaged drives the one-shot path with a real
// short-lived process (`go version`). An explicit route rule on a generic
// command must run it managed rather than passing through. No t.Parallel:
// this test spawns a real OS process (repo rule for exec tests).
func TestRouteShim_OneShotRunsManaged(t *testing.T) {
	d := shimTestDaemon(t)
	project := shimTestProject(t, `shims {
    rules {
        go-version {
            match "go version"
            action "route"
        }
    }
}
`)
	activeSession(t, d.sessionRegistry, "shim-oneshot", project, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := d.routeShim(ctx, &protocol.ShimExecRequest{
		ProjectPath: project, Command: "go", Args: []string{"version"},
	})
	assert.Equal(t, "handled", resp.Action)
	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Output, "go version")
	assert.Contains(t, resp.Message, "managed by agnt")

	// The registry must not accumulate dead one-shot processes.
	procs := d.hub.ProcessManager().List()
	for _, p := range procs {
		assert.NotContains(t, p.ID, "shim-go-version", "one-shot proc should be removed after exit")
	}
}

// TestShimExecProtocolRoundTrip exercises the full socket path: client
// ShimExec → hub SHIM EXEC handler → JSON response.
func TestShimExecProtocolRoundTrip(t *testing.T) {
	d := shimTestDaemon(t)
	project := shimTestProject(t, `shims {
    rules {
        skip-lint {
            match "npm run lint"
            action "ignore"
        }
    }
}
`)
	activeSession(t, d.sessionRegistry, "shim-rt", project, "")

	client := NewClient(WithSocketPath(d.config.SocketPath), WithTimeout(10*time.Second))
	require.NoError(t, client.Connect())
	defer client.Close()

	resp, err := client.ShimExec(protocol.ShimExecRequest{
		ProjectPath: project,
		SessionCode: "shim-rt",
		Command:     "npm",
		Args:        []string{"run", "lint"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "handled", resp.Action)
	assert.Contains(t, resp.Message, "ignored")

	// Unregistered project → passthrough decision over the wire.
	resp, err = client.ShimExec(protocol.ShimExecRequest{
		ProjectPath: t.TempDir(), Command: "npm", Args: []string{"run", "dev"},
	})
	require.NoError(t, err)
	assert.Equal(t, "passthrough", resp.Action)
}

// TestShimRegisterProtocolRoundTrip covers SHIM REGISTER: the manifest
// gains the project entry.
func TestShimRegisterProtocolRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d := shimTestDaemon(t)
	project := t.TempDir()

	client := NewClient(WithSocketPath(d.config.SocketPath), WithTimeout(10*time.Second))
	require.NoError(t, client.Connect())
	defer client.Close()

	require.NoError(t, client.ShimRegister(protocol.ShimRegisterRequest{
		ProjectPath: project,
		BinDir:      filepath.Join(project, ".agnt", "bin"),
		SessionCode: "shim-reg",
	}))

	m := shims.LoadManifest()
	entry := m.Projects[project]
	require.NotNil(t, entry)
	assert.Equal(t, []string{"shim-reg"}, entry.Sessions)
}

// TestRouteShim_KillCrossProjectPasses guards the session-scoping fix:
// killall/pkill must never stop another project's managed processes. When
// every match lives in a different project, the whole command passes
// through to the real binary.
func TestRouteShim_KillCrossProjectPasses(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	projectA := shimTestProject(t, "scripts {}\n")
	projectB := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-xproj", projectA, "")

	// A managed process owned by project B whose command matches the
	// pkill operand.
	pm := d.hub.ProcessManager()
	proc := goprocess.NewManagedProcess(goprocess.ProcessConfig{
		ID: "b-proc", ProjectPath: projectB, Command: "vite",
	})
	require.NoError(t, pm.Register(proc))

	resp := d.routeShim(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: projectA, Command: "pkill", Args: []string{"vite"},
	})
	assert.Equal(t, "passthrough", resp.Action, "pkill matching only another project's procs must pass through")
}

// TestRouteShim_KillMixedPIDsPasses guards the no-partial-application fix:
// kill with one managed and one unmanaged PID must pass the WHOLE command
// through without stopping the managed process. Spawns a real sleep
// process — no t.Parallel per repo exec-test rule.
func TestRouteShim_KillMixedPIDsPasses(t *testing.T) {
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")
	activeSession(t, d.sessionRegistry, "shim-mixed", project, "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pm := d.hub.ProcessManager()
	result, err := pm.StartOrReuse(ctx, goprocess.ProcessConfig{
		ID: "sleeper", ProjectPath: project, Command: "sleep", Args: []string{"30"},
	})
	require.NoError(t, err)
	managedPID := result.Process.PID()
	require.Positive(t, managedPID)

	resp := d.routeShim(ctx, &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "kill",
		Args:        []string{strconv.Itoa(managedPID), "4194304"},
	})
	assert.Equal(t, "passthrough", resp.Action)
	assert.True(t, result.Process.IsRunning(), "managed proc must survive a mixed-target kill")

	// And the all-managed case really stops it.
	resp = d.routeShim(ctx, &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "kill",
		Args:        []string{strconv.Itoa(managedPID)},
	})
	assert.Equal(t, "handled", resp.Action)
}

// TestShimRunOneShot_PostNoteOnStartFailure pins the quiesce guarantee: the
// watch-restart note runs even when the command never starts.
func TestShimRunOneShot_PostNoteOnStartFailure(t *testing.T) {
	t.Parallel()
	d := shimTestDaemon(t)
	project := shimTestProject(t, "scripts {}\n")

	var postRan bool
	resp := d.shimRunOneShot(context.Background(), &protocol.ShimExecRequest{
		ProjectPath: project,
		Command:     "", // StartOrReuse rejects an empty command
	}, "empty", "", func() string { return "stopped watch" }, func() string {
		postRan = true
		return "restarted watch"
	})
	assert.Equal(t, 1, resp.ExitCode)
	assert.True(t, postRan, "postNote must run even when the process fails to start")
	assert.Contains(t, resp.Message, "restarted watch")
}

// TestShimWorkingDir covers the filepath.Rel containment check.
func TestShimWorkingDir(t *testing.T) {
	t.Parallel()
	req := &protocol.ShimExecRequest{ProjectPath: "/proj"}
	assert.Equal(t, "/proj", shimWorkingDir(req))

	req.Cwd = "/proj/sub"
	assert.Equal(t, "/proj/sub", shimWorkingDir(req))

	req.Cwd = "/proj2"
	assert.Equal(t, "/proj", shimWorkingDir(req), "sibling with shared prefix is outside the project")

	req.Cwd = "/elsewhere"
	assert.Equal(t, "/proj", shimWorkingDir(req))
}

// TestShimWatcherAlive verifies PID-recycle protection on the watcher
// liveness probe using the test process itself.
func TestShimWatcherAlive(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	birth, ok := platform.ProcessBirthID(pid)
	if ok {
		assert.True(t, shimWatcherAlive(pid, birth))
		assert.False(t, shimWatcherAlive(pid, "bogus-birth-token"))
	}
	assert.True(t, shimWatcherAlive(pid, ""), "empty recorded birth falls back to PID-only")
	assert.False(t, shimWatcherAlive(4194304, ""), "dead PID is dead")
}

func TestShimScriptName(t *testing.T) {
	t.Parallel()
	project := shimTestProject(t, `scripts {
    dev {
        run "vite"
    }
}
`)
	activeSession(t, shimTestDaemon(t).sessionRegistry, "shim-s7", project, "")

	cfg, err := config.LoadAgntConfig(project)
	require.NoError(t, err)
	req := &protocol.ShimExecRequest{Command: "npm", Args: []string{"run", "dev"}}
	assert.Equal(t, "dev", shimScriptName(req, cfg))

	req = &protocol.ShimExecRequest{Command: "yarn", Args: []string{"dev"}}
	assert.Equal(t, "dev", shimScriptName(req, cfg))

	req = &protocol.ShimExecRequest{Command: "npm", Args: []string{"run", "build"}}
	assert.Equal(t, "", shimScriptName(req, cfg))
}
