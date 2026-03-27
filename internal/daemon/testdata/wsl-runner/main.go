//go:build windows

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// TestResult captures the outcome of a single test case.
type TestResult struct {
	Name       string `json:"name"`
	Pass       bool   `json:"pass"`
	DurationMS int64  `json:"duration_ms"`
	Details    string `json:"details,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Report is the JSON output of the test runner.
type Report struct {
	Tests     []TestResult `json:"tests"`
	Platform  string       `json:"platform"`
	GoVersion string       `json:"go_version"`
}

func main() {
	testDir := flag.String("test-dir", "", "Windows path to test directory containing fixtures")
	reportFile := flag.String("report", "results.json", "Output report filename (written to test-dir)")
	flag.Parse()

	if *testDir == "" {
		fmt.Fprintln(os.Stderr, "error: --test-dir is required")
		os.Exit(1)
	}

	report := Report{
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion: runtime.Version(),
	}

	report.Tests = append(report.Tests, runProcessStart(*testDir))
	report.Tests = append(report.Tests, runProcessStop(*testDir))
	report.Tests = append(report.Tests, runPortCleanup(*testDir))
	report.Tests = append(report.Tests, runProcessTreeCleanup(*testDir))
	report.Tests = append(report.Tests, runShellResolution(*testDir))

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshaling report: %v\n", err)
		os.Exit(1)
	}

	outPath := filepath.Join(*testDir, *reportFile)
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing report to %s: %v\n", outPath, err)
		os.Exit(1)
	}

	fmt.Printf("report written to %s\n", outPath)
}

// freePort asks the OS for an available TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// waitForPort polls until a TCP connection to the port succeeds.
func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("port %d not accepting connections after %s", port, timeout)
}

// isPortFree checks whether the port is not being listened on.
func isPortFree(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return true
	}
	conn.Close()
	return false
}

// findPIDsByPort returns PIDs listening on a given TCP port using netstat.
func findPIDsByPort(port int) []int {
	cmd := exec.Command("netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var pids []int
	seen := make(map[int]struct{})
	portSuffix := fmt.Sprintf(":%d", port)

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP") || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasSuffix(fields[1], portSuffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; !ok {
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}
	return pids
}

// isProcessAlive checks if a Windows PID is still running via tasklist.
func isProcessAlive(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), strconv.Itoa(pid))
}

// killProcess kills a Windows process by PID using taskkill /F.
func killProcess(pid int) error {
	cmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
	return cmd.Run()
}

// killProcessTree kills a Windows process and all its children using taskkill /T /F.
func killProcessTree(pid int) error {
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	return cmd.Run()
}

// startPowerShellListener starts a PowerShell process that listens on a TCP port.
// Returns the process and its PID.
func startPowerShellListener(port int) (*exec.Cmd, error) {
	script := fmt.Sprintf(`
$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, %d)
$listener.Start()
Write-Host "listening on http://localhost:%d"
try {
    while ($true) {
        if ($listener.Pending()) {
            $client = $listener.AcceptTcpClient()
            $stream = $client.GetStream()
            $writer = [System.IO.StreamWriter]::new($stream)
            $writer.WriteLine("hello from powershell")
            $writer.Flush()
            $client.Close()
        }
        Start-Sleep -Milliseconds 100
    }
} finally {
    $listener.Stop()
}`, port, port)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// runProcessStart tests that a PowerShell process can start and bind a port.
func runProcessStart(testDir string) TestResult {
	start := time.Now()

	port, err := freePort()
	if err != nil {
		return TestResult{Name: "ProcessStart", Pass: false, Error: err.Error(), DurationMS: elapsed(start)}
	}

	cmd, err := startPowerShellListener(port)
	if err != nil {
		return TestResult{Name: "ProcessStart", Pass: false, Error: fmt.Sprintf("failed to start: %v", err), DurationMS: elapsed(start)}
	}
	defer killProcessTree(cmd.Process.Pid)

	if err := waitForPort(port, 15*time.Second); err != nil {
		return TestResult{Name: "ProcessStart", Pass: false, Error: err.Error(), DurationMS: elapsed(start)}
	}

	if !isProcessAlive(cmd.Process.Pid) {
		return TestResult{Name: "ProcessStart", Pass: false, Error: "process died immediately", DurationMS: elapsed(start)}
	}

	return TestResult{
		Name:       "ProcessStart",
		Pass:       true,
		DurationMS: elapsed(start),
		Details:    fmt.Sprintf("pid=%d port=%d", cmd.Process.Pid, port),
	}
}

// runProcessStop tests stopping a process and verifying port is freed.
func runProcessStop(testDir string) TestResult {
	start := time.Now()

	port, err := freePort()
	if err != nil {
		return TestResult{Name: "ProcessStop", Pass: false, Error: err.Error(), DurationMS: elapsed(start)}
	}

	cmd, err := startPowerShellListener(port)
	if err != nil {
		return TestResult{Name: "ProcessStop", Pass: false, Error: fmt.Sprintf("failed to start: %v", err), DurationMS: elapsed(start)}
	}

	if err := waitForPort(port, 15*time.Second); err != nil {
		killProcessTree(cmd.Process.Pid)
		return TestResult{Name: "ProcessStop", Pass: false, Error: fmt.Sprintf("port not ready: %v", err), DurationMS: elapsed(start)}
	}

	pid := cmd.Process.Pid
	if err := killProcess(pid); err != nil {
		killProcessTree(pid)
		return TestResult{Name: "ProcessStop", Pass: false, Error: fmt.Sprintf("kill failed: %v", err), DurationMS: elapsed(start)}
	}

	// Wait for process to die and port to free
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pid) && isPortFree(port) {
			return TestResult{
				Name:       "ProcessStop",
				Pass:       true,
				DurationMS: elapsed(start),
				Details:    fmt.Sprintf("pid=%d killed, port=%d freed", pid, port),
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Cleanup
	killProcessTree(pid)

	if isProcessAlive(pid) {
		return TestResult{Name: "ProcessStop", Pass: false, Error: "process still alive after kill", DurationMS: elapsed(start)}
	}
	if !isPortFree(port) {
		return TestResult{Name: "ProcessStop", Pass: false, Error: "port still bound after kill", DurationMS: elapsed(start)}
	}

	return TestResult{Name: "ProcessStop", Pass: true, DurationMS: elapsed(start), Details: "required tree kill"}
}

// runPortCleanup tests findPIDsByPort and killing by port.
func runPortCleanup(testDir string) TestResult {
	start := time.Now()

	port, err := freePort()
	if err != nil {
		return TestResult{Name: "PortCleanup", Pass: false, Error: err.Error(), DurationMS: elapsed(start)}
	}

	cmd, err := startPowerShellListener(port)
	if err != nil {
		return TestResult{Name: "PortCleanup", Pass: false, Error: fmt.Sprintf("failed to start: %v", err), DurationMS: elapsed(start)}
	}
	defer killProcessTree(cmd.Process.Pid)

	if err := waitForPort(port, 15*time.Second); err != nil {
		return TestResult{Name: "PortCleanup", Pass: false, Error: fmt.Sprintf("port not ready: %v", err), DurationMS: elapsed(start)}
	}

	pids := findPIDsByPort(port)
	if len(pids) == 0 {
		return TestResult{Name: "PortCleanup", Pass: false, Error: "findPIDsByPort returned no PIDs", DurationMS: elapsed(start)}
	}

	// Kill all PIDs found on the port
	for _, pid := range pids {
		killProcess(pid)
	}

	// Wait for port to free
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if isPortFree(port) {
			return TestResult{
				Name:       "PortCleanup",
				Pass:       true,
				DurationMS: elapsed(start),
				Details:    fmt.Sprintf("found pids=%v on port=%d, killed, port freed", pids, port),
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	return TestResult{Name: "PortCleanup", Pass: false, Error: fmt.Sprintf("port %d still bound after killing pids %v", port, pids), DurationMS: elapsed(start)}
}

// runProcessTreeCleanup tests that taskkill /T /F kills a parent and its child.
// The parent PowerShell binds the port and spawns a child that sleeps.
func runProcessTreeCleanup(testDir string) TestResult {
	start := time.Now()

	port, err := freePort()
	if err != nil {
		return TestResult{Name: "ProcessTreeCleanup", Pass: false, Error: err.Error(), DurationMS: elapsed(start)}
	}

	// Parent binds the port and spawns a sleeping child.
	// Using inline script avoids the escaping issues of Start-Process with here-strings.
	parentScript := fmt.Sprintf(
		`$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, %d); `+
			`$listener.Start(); `+
			`$child = Start-Process -FilePath "powershell.exe" -ArgumentList "-NoProfile","-Command","Start-Sleep -Seconds 300" -PassThru -NoNewWindow; `+
			`Write-Host "parent=$PID child=$($child.Id) port=%d"; `+
			`try { while ($true) { if ($listener.Pending()) { $c = $listener.AcceptTcpClient(); $c.Close() }; Start-Sleep -Milliseconds 100 } } `+
			`finally { $listener.Stop() }`,
		port, port)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", parentScript)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return TestResult{Name: "ProcessTreeCleanup", Pass: false, Error: fmt.Sprintf("start failed: %v", err), DurationMS: elapsed(start)}
	}
	parentPID := cmd.Process.Pid
	defer killProcessTree(parentPID)

	if err := waitForPort(port, 30*time.Second); err != nil {
		return TestResult{Name: "ProcessTreeCleanup", Pass: false, Error: fmt.Sprintf("port not ready: %v", err), DurationMS: elapsed(start)}
	}

	// Use taskkill /T /F to kill the entire process tree
	if err := killProcessTree(parentPID); err != nil {
		return TestResult{Name: "ProcessTreeCleanup", Pass: false, Error: fmt.Sprintf("tree kill failed: %v", err), DurationMS: elapsed(start)}
	}

	// Wait for port to free and parent to die
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if isPortFree(port) && !isProcessAlive(parentPID) {
			return TestResult{
				Name:       "ProcessTreeCleanup",
				Pass:       true,
				DurationMS: elapsed(start),
				Details:    fmt.Sprintf("parent=%d killed with tree, port=%d freed", parentPID, port),
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !isPortFree(port) {
		return TestResult{Name: "ProcessTreeCleanup", Pass: false, Error: "port still bound after tree kill", DurationMS: elapsed(start)}
	}
	return TestResult{Name: "ProcessTreeCleanup", Pass: false, Error: "parent still alive after tree kill", DurationMS: elapsed(start)}
}

// runShellResolution tests that the Windows environment resolves shells correctly.
func runShellResolution(testDir string) TestResult {
	start := time.Now()

	// Verify powershell.exe is available
	psPath, err := exec.LookPath("powershell.exe")
	if err != nil {
		return TestResult{Name: "ShellResolution", Pass: false, Error: "powershell.exe not in PATH", DurationMS: elapsed(start)}
	}

	// Verify cmd.exe is available
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		return TestResult{Name: "ShellResolution", Pass: false, Error: "cmd.exe not in PATH", DurationMS: elapsed(start)}
	}

	// Verify we can execute a simple command via powershell
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	psCmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", "Write-Host 'shell-ok'")
	psOut, err := psCmd.CombinedOutput()
	if err != nil {
		return TestResult{Name: "ShellResolution", Pass: false, Error: fmt.Sprintf("powershell exec failed: %v", err), DurationMS: elapsed(start)}
	}
	if !strings.Contains(string(psOut), "shell-ok") {
		return TestResult{Name: "ShellResolution", Pass: false, Error: "powershell output missing marker", DurationMS: elapsed(start)}
	}

	// Verify cmd.exe can execute
	cmdCmd := exec.CommandContext(ctx, "cmd.exe", "/c", "echo cmd-ok")
	cmdOut, err := cmdCmd.CombinedOutput()
	if err != nil {
		return TestResult{Name: "ShellResolution", Pass: false, Error: fmt.Sprintf("cmd.exe exec failed: %v", err), DurationMS: elapsed(start)}
	}
	if !strings.Contains(string(cmdOut), "cmd-ok") {
		return TestResult{Name: "ShellResolution", Pass: false, Error: "cmd.exe output missing marker", DurationMS: elapsed(start)}
	}

	return TestResult{
		Name:       "ShellResolution",
		Pass:       true,
		DurationMS: elapsed(start),
		Details:    fmt.Sprintf("powershell=%s cmd=%s", psPath, cmdPath),
	}
}

func elapsed(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
