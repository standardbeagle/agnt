package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// ANSI color codes for terminal output.
const (
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
	colorRed    = "\x1b[31m"
	colorReset  = "\x1b[0m"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run system diagnostics",
	Long: `Check for orphaned processes, port conflicts, daemon health, and other issues.

When the daemon is running, performs comprehensive checks via the daemon.
Without the daemon, runs OS-level checks using the PID tracker and config files.`,
	RunE: runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	socketPath := getSocketPath(cmd)
	projectPath, _ := os.Getwd()

	// Try daemon first for comprehensive checks. Attempt a direct connect
	// instead of a separate IsRunning probe — the probe dials and closes a
	// socket whose handler goroutine races with the real doctor connect, so
	// clientCount briefly reads both connections and makes the output say
	// "2 client(s)" when only the doctor is really connected.
	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		// Daemon not available — run standalone OS-level checks.
		return runDoctorStandalone(projectPath)
	}
	return runDoctorViaDaemon(client, projectPath)
}

// runDoctorViaDaemon runs the daemon-side checks via an already-connected
// client and prints the report. The caller owns the client and must provide
// one that is already Connect()'d.
func runDoctorViaDaemon(client *daemonclient.Client, projectPath string) error {
	defer client.Close()

	result, err := client.Doctor(projectPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Daemon doctor failed, falling back to standalone: %v\n", err)
		return runDoctorStandalone(projectPath)
	}

	// Parse the daemon report
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal daemon report: %w", err)
	}

	var report daemon.DoctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("failed to parse daemon report: %w", err)
	}

	printReport(&report)
	return exitForStatus(report.Status)
}

// runDoctorStandalone performs OS-level checks without a daemon.
func runDoctorStandalone(projectPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var checks []daemon.CheckResult

	checks = append(checks, checkDaemonStandalone())
	checks = append(checks, checkOrphansStandalone(ctx))
	checks = append(checks, checkConfigStandalone(ctx, projectPath))

	overall := daemon.StatusOK
	for _, c := range checks {
		overall = pickWorst(overall, c.Status)
	}

	report := &daemon.DoctorReport{
		Status: overall,
		Checks: checks,
	}

	printReport(report)
	return exitForStatus(report.Status)
}

// checkDaemonStandalone checks if the daemon process is alive via the PID tracker.
func checkDaemonStandalone() daemon.CheckResult {
	tracker := goprocess.NewFilePIDTracker(goprocess.FilePIDTrackerConfig{
		AppName: "devtool-mcp",
	})
	tracking := tracker.Load()

	if tracking.DaemonPID <= 0 {
		return daemon.CheckResult{
			Name:    "daemon_health",
			Status:  daemon.StatusWarning,
			Message: "not running (no PID recorded)",
		}
	}

	if !processAlive(tracking.DaemonPID) {
		return daemon.CheckResult{
			Name:    "daemon_health",
			Status:  daemon.StatusWarning,
			Message: fmt.Sprintf("not running (stale PID %d)", tracking.DaemonPID),
			Fix:     "agnt up",
		}
	}

	return daemon.CheckResult{
		Name:    "daemon_health",
		Status:  daemon.StatusWarning,
		Message: fmt.Sprintf("PID %d alive but not responding on socket", tracking.DaemonPID),
		Fix:     "agnt daemon restart",
	}
}

// checkOrphansStandalone reads the PID tracker and checks for orphaned processes.
func checkOrphansStandalone(ctx context.Context) daemon.CheckResult {
	tracker := goprocess.NewFilePIDTracker(goprocess.FilePIDTrackerConfig{
		AppName: "devtool-mcp",
	})
	tracked := tracker.ListTracked()

	if len(tracked) == 0 {
		return daemon.CheckResult{
			Name:    "orphan_processes",
			Status:  daemon.StatusOK,
			Message: "no tracked processes",
		}
	}

	var orphans []map[string]interface{}
	for _, tp := range tracked {
		if processAlive(tp.PID) {
			orphans = append(orphans, map[string]interface{}{
				"id":  tp.ID,
				"pid": tp.PID,
			})
		}
	}

	if len(orphans) == 0 {
		return daemon.CheckResult{
			Name:    "orphan_processes",
			Status:  daemon.StatusOK,
			Message: fmt.Sprintf("%d tracked, none alive (daemon not running to manage)", len(tracked)),
		}
	}

	return daemon.CheckResult{
		Name:    "orphan_processes",
		Status:  daemon.StatusWarning,
		Message: fmt.Sprintf("%d process(es) alive but daemon not running to manage them", len(orphans)),
		Details: orphans,
		Fix:     "agnt up (to reclaim) or kill manually",
	}
}

// checkConfigStandalone validates .agnt.kdl and checks declared ports.
func checkConfigStandalone(ctx context.Context, projectPath string) daemon.CheckResult {
	configPath := config.FindAgntConfigFile(projectPath)
	if configPath == "" {
		return daemon.CheckResult{
			Name:    "config_health",
			Status:  daemon.StatusOK,
			Message: "no .agnt.kdl found",
		}
	}

	cfg, err := config.LoadAgntConfigFile(configPath)
	if err != nil {
		return daemon.CheckResult{
			Name:    "config_health",
			Status:  daemon.StatusError,
			Message: fmt.Sprintf("config parse error: %v", err),
			Fix:     "fix .agnt.kdl syntax",
		}
	}

	// Check ports declared in scripts for conflicts
	var conflicts []string
	for scriptName, sc := range cfg.Scripts {
		for _, port := range sc.Ports {
			if portInUse(port) {
				pids := findPIDsByPort(ctx, port)
				if len(pids) > 0 {
					conflicts = append(conflicts, fmt.Sprintf(
						"port %d (%s): PIDs %v", port, scriptName, pids))
				} else {
					conflicts = append(conflicts, fmt.Sprintf(
						"port %d (%s): in use", port, scriptName))
				}
			}
		}
	}

	summary := fmt.Sprintf("valid, %d script(s), %d proxy(ies)", len(cfg.Scripts), len(cfg.Proxies))
	if len(conflicts) > 0 {
		return daemon.CheckResult{
			Name:    "config_health",
			Status:  daemon.StatusWarning,
			Message: fmt.Sprintf("%s; %d port conflict(s)", summary, len(conflicts)),
			Details: conflicts,
			Fix:     "agnt proc cleanup_port PORT",
		}
	}

	return daemon.CheckResult{
		Name:    "config_health",
		Status:  daemon.StatusOK,
		Message: summary,
	}
}

// printReport formats and prints a doctor report with color.
func printReport(report *daemon.DoctorReport) {
	fmt.Println("agnt doctor")
	fmt.Println()
	for _, c := range report.Checks {
		printCheck(c)
	}
}

// printCheck prints a single check result with color and optional fix hint.
func printCheck(c daemon.CheckResult) {
	var prefix, color string
	switch c.Status {
	case daemon.StatusOK:
		prefix = "\xe2\x9c\x93" // check mark
		color = colorGreen
	case daemon.StatusWarning:
		prefix = "\xe2\x9a\xa0" // warning sign
		color = colorYellow
	default:
		prefix = "\xe2\x9c\x97" // x mark
		color = colorRed
	}

	fmt.Printf("%s%s %s: %s%s\n", color, prefix, c.Name, c.Message, colorReset)
	if c.Fix != "" {
		fmt.Printf("  fix: %s\n", c.Fix)
	}
}

// exitForStatus returns a sentinel error that cobra translates to the right exit code.
func exitForStatus(status string) error {
	switch status {
	case daemon.StatusWarning:
		os.Exit(1)
	case daemon.StatusError:
		os.Exit(2)
	}
	return nil
}

// pickWorst returns the more severe of two statuses.
func pickWorst(a, b string) string {
	rank := map[string]int{daemon.StatusOK: 0, daemon.StatusWarning: 1, daemon.StatusError: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// processAlive checks whether a PID is still running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// portInUse checks if a TCP port is currently listening.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// findPIDsByPort finds PIDs listening on a port using lsof or ss.
func findPIDsByPort(ctx context.Context, port int) []int {
	// Try lsof first
	cmd := exec.CommandContext(ctx, "lsof", "-ti", fmt.Sprintf(":%d", port))
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		return parsePIDOutput(strings.TrimSpace(string(output)))
	}

	// Fall back to ss
	cmd = exec.CommandContext(ctx, "ss", "-tlnp")
	output, err = cmd.Output()
	if err != nil {
		return nil
	}

	var pids []int
	portPattern := fmt.Sprintf(":%d", port)
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.Contains(line, portPattern) {
			continue
		}
		start := strings.Index(line, "pid=")
		if start == -1 {
			continue
		}
		start += 4
		end := strings.IndexAny(line[start:], ",)")
		if end == -1 {
			continue
		}
		if pid, err := strconv.Atoi(line[start : start+end]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// parsePIDOutput parses newline-separated PID strings.
func parsePIDOutput(output string) []int {
	if output == "" {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
