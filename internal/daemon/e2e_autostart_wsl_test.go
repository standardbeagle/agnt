//go:build e2e && linux

package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wslRunnerResult mirrors the Report struct in the wsl-runner binary.
type wslRunnerResult struct {
	Tests []struct {
		Name       string `json:"name"`
		Pass       bool   `json:"pass"`
		DurationMS int64  `json:"duration_ms"`
		Details    string `json:"details,omitempty"`
		Error      string `json:"error,omitempty"`
	} `json:"tests"`
	Platform  string `json:"platform"`
	GoVersion string `json:"go_version"`
}

// skipIfNotWSL skips the test if the environment is not WSL.
func skipIfNotWSL(t *testing.T) {
	t.Helper()

	// Check /proc/version for WSL signature
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		t.Skip("cannot read /proc/version, not Linux")
	}
	version := strings.ToLower(string(data))
	if !strings.Contains(version, "microsoft") && !strings.Contains(version, "wsl") {
		t.Skip("/proc/version does not indicate WSL")
	}

	// Check that /mnt/c exists (Windows C: drive mount)
	if _, err := os.Stat("/mnt/c"); os.IsNotExist(err) {
		t.Skip("/mnt/c does not exist, not WSL with Windows mount")
	}

	// Check that powershell.exe is reachable
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("powershell.exe not in PATH, WSL interop may be disabled")
	}
}

// buildWSLRunner cross-compiles the Windows test runner binary.
// Returns the path to the built .exe (inside testdata).
func buildWSLRunner(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	projectRoot := filepath.Join(wd, "..", "..")

	// Output into a temp dir so parallel tests don't collide
	outDir := t.TempDir()
	exePath := filepath.Join(outDir, "wsl-test-runner.exe")

	runnerPkg := filepath.Join(projectRoot, "internal", "daemon", "testdata", "wsl-runner")
	cmd := exec.Command("go", "build", "-o", exePath, runnerPkg)
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "cross-compile wsl-runner failed: %s", string(out))
	require.FileExists(t, exePath)

	return exePath
}

// createWindowsTempDir creates a temp directory on the Windows filesystem.
// Uses /mnt/c/tmp which maps to C:\tmp on Windows.
func createWindowsTempDir(t *testing.T) (wslPath, winPath string) {
	t.Helper()

	basePath := "/mnt/c/tmp"
	if err := os.MkdirAll(basePath, 0755); err != nil {
		t.Skipf("cannot create %s: %v (may need permissions)", basePath, err)
	}

	dirName := fmt.Sprintf("agnt-e2e-%d", time.Now().UnixNano())
	wslPath = filepath.Join(basePath, dirName)
	require.NoError(t, os.MkdirAll(wslPath, 0755))

	// Convert WSL path to Windows path: /mnt/c/tmp/xxx -> C:\tmp\xxx
	winPath = `C:\tmp\` + dirName

	t.Cleanup(func() {
		os.RemoveAll(wslPath)
	})

	return wslPath, winPath
}

// TestE2E_WSL_Runner executes the cross-compiled Windows test runner from WSL
// and validates each test case in the JSON report.
func TestE2E_WSL_Runner(t *testing.T) {
	skipIfNotWSL(t)

	exePath := buildWSLRunner(t)
	wslTmpDir, winTmpDir := createWindowsTempDir(t)

	// Execute the Windows binary from WSL (WSL handles .exe translation)
	reportName := "results.json"
	cmd := exec.Command(exePath, "--test-dir", winTmpDir, "--report", reportName)
	cmd.Dir = wslTmpDir

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "wsl-runner failed: %s", string(out))

	// Read the JSON report (file is at wslTmpDir/results.json since wsl translates)
	reportPath := filepath.Join(wslTmpDir, reportName)
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err, "cannot read report at %s", reportPath)

	var report wslRunnerResult
	require.NoError(t, json.Unmarshal(reportData, &report), "invalid report JSON")

	assert.Equal(t, "windows/amd64", report.Platform, "runner should report windows platform")
	assert.NotEmpty(t, report.GoVersion, "go version should be set")

	// Each test in the report becomes a Go subtest
	for _, tc := range report.Tests {
		t.Run(tc.Name, func(t *testing.T) {
			if !tc.Pass {
				t.Errorf("FAIL [%dms]: %s", tc.DurationMS, tc.Error)
			} else {
				t.Logf("PASS [%dms]: %s", tc.DurationMS, tc.Details)
			}
		})
	}

	// Verify all expected tests ran
	expectedTests := []string{"ProcessStart", "ProcessStop", "PortCleanup", "ProcessTreeCleanup", "ShellResolution"}
	testNames := make(map[string]bool)
	for _, tc := range report.Tests {
		testNames[tc.Name] = true
	}
	for _, name := range expectedTests {
		assert.True(t, testNames[name], "expected test %q missing from report", name)
	}
}

// TestE2E_WSL_ShellResolution tests that ScriptConfig.ResolveShell returns
// the correct shell for Windows contexts.
func TestE2E_WSL_ShellResolution(t *testing.T) {
	skipIfNotWSL(t)

	t.Run("explicit_powershell_shell", func(t *testing.T) {
		cfg := &scriptConfigForTest{
			Shell: "powershell.exe",
			Run:   "& ./test.ps1",
		}
		shell, args := cfg.resolveShell()
		assert.Equal(t, "powershell.exe", shell)
		assert.Contains(t, args, "-Command")
		assert.Contains(t, args, "& ./test.ps1")
	})

	t.Run("explicit_powershell_with_shell_args", func(t *testing.T) {
		cfg := &scriptConfigForTest{
			Shell:     "powershell.exe",
			ShellArgs: []string{"-NoProfile", "-Command"},
			Run:       "& ./test.ps1",
		}
		shell, args := cfg.resolveShell()
		assert.Equal(t, "powershell.exe", shell)
		assert.Equal(t, []string{"-NoProfile", "-Command", "& ./test.ps1"}, args)
	})

	t.Run("explicit_cmd_shell", func(t *testing.T) {
		cfg := &scriptConfigForTest{
			Shell: "cmd.exe",
			Run:   "dir",
		}
		shell, args := cfg.resolveShell()
		assert.Equal(t, "cmd.exe", shell)
		assert.Contains(t, args, "/c")
		assert.Contains(t, args, "dir")
	})
}

// scriptConfigForTest mirrors config.ScriptConfig.ResolveShell logic
// to test shell resolution without importing the config package
// (which would create a test dependency on KDL parsing).
type scriptConfigForTest struct {
	Run       string
	Shell     string
	ShellArgs []string
}

func (s *scriptConfigForTest) resolveShell() (string, []string) {
	if s.Shell != "" {
		if len(s.ShellArgs) > 0 {
			return s.Shell, append(s.ShellArgs, s.Run)
		}
		base := strings.ToLower(filepath.Base(s.Shell))
		base = strings.TrimSuffix(base, ".exe")
		switch base {
		case "cmd":
			return s.Shell, []string{"/c", s.Run}
		case "powershell", "pwsh":
			return s.Shell, []string{"-NoLogo", "-Command", s.Run}
		default:
			return s.Shell, []string{"-c", s.Run}
		}
	}
	return "sh", []string{"-c", s.Run}
}
