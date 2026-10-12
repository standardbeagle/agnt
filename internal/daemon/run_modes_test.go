package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// shellExec is the direct-exec argv that runs line through the platform shell.
func shellExec(line string) (command string, args []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", line}
	}
	return "sh", []string{"-c", line}
}

func resultInt(t *testing.T, result map[string]interface{}, key string) int {
	t.Helper()
	v, ok := result[key].(float64)
	if !ok {
		t.Fatalf("result has no numeric %q: %v", key, result)
	}
	return int(v)
}

// A foreground run reports how the process ended. Before the fix the daemon
// ignored Mode and answered "running" with no exit code or output.
func TestRun_ForegroundModesWaitForExit(t *testing.T) {
	// No t.Parallel(): starts real processes.
	_, client, tmpDir := newHubProcTestDaemon(t)
	command, args := shellExec("echo fg-out&& echo fg-err 1>&2&& exit 7")

	run := func(t *testing.T, id, mode string) map[string]interface{} {
		t.Helper()
		result, err := client.Run(protocol.RunConfig{
			ID: id, Path: tmpDir, Raw: true, Command: command, Args: args, Mode: mode,
		})
		if err != nil {
			t.Fatalf("RUN mode=%s: %v", mode, err)
		}
		return result
	}

	t.Run("foreground-raw returns exit code and output", func(t *testing.T) {
		result := run(t, "fg-raw", "foreground-raw")
		if code := resultInt(t, result, "exit_code"); code != 7 {
			t.Errorf("exit_code = %d, want 7", code)
		}
		if state := result["state"]; state == "running" || state == "starting" {
			t.Errorf("state = %v, want a terminal state", state)
		}
		if out, _ := result["stdout"].(string); !strings.Contains(out, "fg-out") {
			t.Errorf("stdout = %q, want it to contain fg-out", out)
		}
		if errOut, _ := result["stderr"].(string); !strings.Contains(errOut, "fg-err") {
			t.Errorf("stderr = %q, want it to contain fg-err", errOut)
		}
		if _, ok := result["runtime"].(string); !ok {
			t.Errorf("runtime missing: %v", result)
		}
	})

	t.Run("foreground returns exit code without output", func(t *testing.T) {
		result := run(t, "fg", "foreground")
		if code := resultInt(t, result, "exit_code"); code != 7 {
			t.Errorf("exit_code = %d, want 7", code)
		}
		if _, has := result["stdout"]; has {
			t.Errorf("foreground must not carry stdout (that is foreground-raw): %v", result)
		}
	})

	t.Run("unknown mode is refused", func(t *testing.T) {
		if _, err := client.Run(protocol.RunConfig{
			ID: "bad-mode", Path: tmpDir, Raw: true, Command: command, Args: args, Mode: "detached",
		}); err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Fatalf("err = %v, want an unknown-mode refusal", err)
		}
	})
}

// A script started by name runs with what .agnt.kdl declares for it. The MCP
// run tool used to forward only the resolved command line, losing env and cwd.
func TestProcRun_FromConfigAppliesScriptConfiguration(t *testing.T) {
	// No t.Parallel(): starts real processes.
	_, client, tmpDir := newHubProcTestDaemon(t)
	if err := os.Mkdir(filepath.Join(tmpDir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `echo var=$AGNT_RUN_TEST_VAR; pwd`
	if runtime.GOOS == "windows" {
		line = `echo var=%AGNT_RUN_TEST_VAR%& cd`
	}
	kdl := fmt.Sprintf("scripts {\n    report {\n        run %q\n        cwd \"svc\"\n        env {\n            AGNT_RUN_TEST_VAR \"from-kdl\"\n        }\n    }\n}\n", line)
	if err := os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := client.ProcRun("report", daemonclient.ProcRunConfig{
		ProjectPath: tmpDir, FromConfig: true, Mode: "foreground-raw",
	})
	if err != nil {
		t.Fatalf("PROC RUN from_config: %v", err)
	}
	if code := resultInt(t, result, "exit_code"); code != 0 {
		t.Errorf("exit_code = %d, want 0 (%v)", code, result)
	}
	out, _ := result["stdout"].(string)
	if !strings.Contains(out, "var=from-kdl") {
		t.Errorf("script env was not applied; stdout = %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "svc") {
		t.Errorf("script cwd was not applied; stdout = %q", out)
	}

	t.Run("undeclared script is refused", func(t *testing.T) {
		if _, err := client.ProcRun("nope", daemonclient.ProcRunConfig{ProjectPath: tmpDir, FromConfig: true}); err == nil ||
			!strings.Contains(err.Error(), "not declared") {
			t.Fatalf("err = %v, want a not-declared refusal", err)
		}
	})

	t.Run("inline fields are not combined with from_config", func(t *testing.T) {
		if _, err := client.ProcRun("report", daemonclient.ProcRunConfig{
			ProjectPath: tmpDir, FromConfig: true, Cwd: "elsewhere",
		}); err == nil || !strings.Contains(err.Error(), "from_config") {
			t.Fatalf("err = %v, want the from_config conflict refusal", err)
		}
	})
}
