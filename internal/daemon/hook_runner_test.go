package daemon

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLifecycleHook_SetsEnvVars(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	tmp, err := os.CreateTemp(t.TempDir(), "hook-env-*.txt")
	require.NoError(t, err)
	tmp.Close()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `pwsh -Command "$env:AGNT_EVENT + '|' + $env:AGNT_SCRIPT_ID | Out-File -FilePath '` + tmp.Name() + `' -NoNewline"`
	} else {
		cmd = `printf '%s|%s' "$AGNT_EVENT" "$AGNT_SCRIPT_ID" > ` + tmp.Name()
	}

	err = RunLifecycleHook(cmd, "mybackend", "start", &config.ScriptConfig{}, 0)
	require.NoError(t, err)

	data, _ := os.ReadFile(tmp.Name())
	content := strings.TrimSpace(string(data))
	assert.Equal(t, "start|mybackend", content)
}

func TestRunLifecycleHook_RespectsTimeout(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 60"
	} else {
		cmd = "sleep 60"
	}
	start := time.Now()
	err := RunLifecycleHook(cmd, "backend", "stop", &config.ScriptConfig{}, 0)
	elapsed := time.Since(start)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	assert.Less(t, elapsed, 7*time.Second, "must not block longer than 5s timeout + buffer")
}

func TestRunLifecycleHook_ExitCodeEnvVar(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	tmp, err := os.CreateTemp(t.TempDir(), "hook-exitcode-*.txt")
	require.NoError(t, err)
	tmp.Close()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `pwsh -Command "$env:AGNT_EXIT_CODE | Out-File -FilePath '` + tmp.Name() + `' -NoNewline"`
	} else {
		cmd = `printf '%s' "$AGNT_EXIT_CODE" > ` + tmp.Name()
	}

	err = RunLifecycleHook(cmd, "svc", "crash", &config.ScriptConfig{}, 137)
	require.NoError(t, err)

	data, _ := os.ReadFile(tmp.Name())
	assert.Contains(t, strings.TrimSpace(string(data)), "137")
}

func TestRunLifecycleHook_InheritsScriptEnv(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	tmp, err := os.CreateTemp(t.TempDir(), "hook-custenv-*.txt")
	require.NoError(t, err)
	tmp.Close()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `pwsh -Command "$env:MY_CUSTOM_VAR | Out-File -FilePath '` + tmp.Name() + `' -NoNewline"`
	} else {
		cmd = `printf '%s' "$MY_CUSTOM_VAR" > ` + tmp.Name()
	}

	scriptCfg := &config.ScriptConfig{
		Env: map[string]string{"MY_CUSTOM_VAR": "hello-from-script"},
	}
	err = RunLifecycleHook(cmd, "svc", "start", scriptCfg, 0)
	require.NoError(t, err)

	data, _ := os.ReadFile(tmp.Name())
	assert.Contains(t, strings.TrimSpace(string(data)), "hello-from-script")
}

func TestRunLifecycleHook_EmptyCmd_NoOp(t *testing.T) {
	err := RunLifecycleHook("", "svc", "start", &config.ScriptConfig{}, 0)
	assert.NoError(t, err)
}
