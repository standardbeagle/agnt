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

func hookTestScriptConfig(env map[string]string) *config.ScriptConfig {
	scriptCfg := &config.ScriptConfig{Env: env}
	if runtime.GOOS == "windows" {
		scriptCfg.Shell = "powershell.exe"
	}
	return scriptCfg
}

func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func TestRunLifecycleHook_SetsEnvVars(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	tmp, err := os.CreateTemp(t.TempDir(), "hook-env-*.txt")
	require.NoError(t, err)
	tmp.Close()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `$env:AGNT_EVENT + '|' + $env:AGNT_SCRIPT_ID | Set-Content -LiteralPath ` + psSingleQuote(tmp.Name()) + ` -NoNewline -Encoding ASCII`
	} else {
		cmd = `printf '%s|%s' "$AGNT_EVENT" "$AGNT_SCRIPT_ID" > ` + tmp.Name()
	}

	err = RunLifecycleHook(cmd, "mybackend", "start", hookTestScriptConfig(nil), 0)
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
		cmd = stayAliveCmd()
	}
	// The property under test is "an overrunning hook is killed and reports
	// timeout", not "the default window is 5 seconds". Injecting a short
	// deadline exercises exactly that property; paying the production 5s
	// window to infer the constant from elapsed time cost 5s of gate time on
	// every run and asserted the weaker thing.
	const testTimeout = 200 * time.Millisecond
	start := time.Now()
	err := runLifecycleHookWithTimeout(testTimeout, cmd, "backend", "stop", hookTestScriptConfig(nil), 0)
	elapsed := time.Since(start)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	// The hook sleeps stayAliveSeconds (20s), well past this 10s ceiling, so
	// finishing under 10s proves the deadline killed it rather than the sleep
	// running to completion. A generous ceiling, not a latency budget.
	assert.Less(t, elapsed, 10*time.Second, "overrunning hook must be killed by its deadline, not run to completion")

	// Pin the production default directly. This is what the old elapsed-time
	// assertion was indirectly (and loosely) guarding, asserted for free.
	assert.Equal(t, 5*time.Second, hookTimeout, "production lifecycle-hook kill deadline")
}

func TestRunLifecycleHook_ExitCodeEnvVar(t *testing.T) {
	// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.
	tmp, err := os.CreateTemp(t.TempDir(), "hook-exitcode-*.txt")
	require.NoError(t, err)
	tmp.Close()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `$env:AGNT_EXIT_CODE | Set-Content -LiteralPath ` + psSingleQuote(tmp.Name()) + ` -NoNewline -Encoding ASCII`
	} else {
		cmd = `printf '%s' "$AGNT_EXIT_CODE" > ` + tmp.Name()
	}

	err = RunLifecycleHook(cmd, "svc", "crash", hookTestScriptConfig(nil), 137)
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
		cmd = `$env:MY_CUSTOM_VAR | Set-Content -LiteralPath ` + psSingleQuote(tmp.Name()) + ` -NoNewline -Encoding ASCII`
	} else {
		cmd = `printf '%s' "$MY_CUSTOM_VAR" > ` + tmp.Name()
	}

	scriptCfg := hookTestScriptConfig(map[string]string{"MY_CUSTOM_VAR": "hello-from-script"})
	err = RunLifecycleHook(cmd, "svc", "start", scriptCfg, 0)
	require.NoError(t, err)

	data, _ := os.ReadFile(tmp.Name())
	assert.Contains(t, strings.TrimSpace(string(data)), "hello-from-script")
}

func TestRunLifecycleHook_EmptyCmd_NoOp(t *testing.T) {
	err := RunLifecycleHook("", "svc", "start", &config.ScriptConfig{}, 0)
	assert.NoError(t, err)
}
