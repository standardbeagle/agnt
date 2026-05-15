package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
)

var errorsCmd = &cobra.Command{
	Use:   "errors",
	Short: "Add external failures to the agnt error queue",
	Long: `Add CI/CD, log, and arbitrary external failures to the same queue used by get_errors.

Use add for one explicit error, or ingest to scan stdin and enqueue matching lines.`,
}

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "Monitor CI/CD output and send failures to the error queue",
	Long: `Monitor CI/CD output from GitHub or any command and send detected failures to the error queue.

Autostart examples for .agnt.kdl:
  scripts {
    ci-github { run "agnt ci github --repo owner/repo --run latest --interval 30s"; autostart true }
    ci-buildkite { run "buildkite-agent pipeline watch | agnt errors ingest --source buildkite"; autostart true }
    ci-generic { run "your-ci-watch-command | agnt errors ingest --source ci"; autostart true }
  }`,
}

var (
	errorSource      string
	errorSeverity    string
	errorCategory    string
	errorDescription string
	errorProjectPath string
	errorPattern     string
	errorIncludeAll  bool

	ciRepo     string
	ciRun      string
	ciInterval time.Duration
)

func init() {
	errorsCmd.AddCommand(errorsAddCmd)
	errorsCmd.AddCommand(errorsIngestCmd)
	addErrorFlags(errorsAddCmd)
	addErrorFlags(errorsIngestCmd)
	errorsIngestCmd.Flags().StringVar(&errorPattern, "pattern", defaultErrorPattern, "Regex used to identify error lines")
	errorsIngestCmd.Flags().BoolVar(&errorIncludeAll, "all", false, "Queue every input line instead of matching the error regex")

	ciCmd.AddCommand(ciExecCmd)
	ciCmd.AddCommand(ciGithubCmd)
	addErrorFlags(ciExecCmd)
	ciExecCmd.Flags().StringVar(&errorPattern, "pattern", defaultErrorPattern, "Regex used to identify error lines")
	ciExecCmd.Flags().BoolVar(&errorIncludeAll, "all", false, "Queue every output line instead of matching the error regex")

	addErrorFlags(ciGithubCmd)
	ciGithubCmd.Flags().StringVar(&ciRepo, "repo", "", "GitHub repository in owner/name form (defaults to gh's current repo)")
	ciGithubCmd.Flags().StringVar(&ciRun, "run", "latest", "GitHub Actions run id or latest")
	ciGithubCmd.Flags().DurationVar(&ciInterval, "interval", 30*time.Second, "Polling interval for gh run watch")
}

func addErrorFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&errorSource, "source", "external", "Source name stored as the queue process id")
	cmd.Flags().StringVar(&errorSeverity, "severity", "error", "Severity: error, warning, or info")
	cmd.Flags().StringVar(&errorCategory, "category", "external", "Error category")
	cmd.Flags().StringVar(&errorDescription, "description", "external error", "Short error description")
	cmd.Flags().StringVar(&errorProjectPath, "project", "", "Project path for get_errors filtering (default: current directory)")
}

var errorsAddCmd = &cobra.Command{
	Use:   "add [message]",
	Short: "Add one message to the error queue",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return reportExternalError(cmd, strings.Join(args, " "))
	},
}

var errorsIngestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Read stdin and queue matching error lines",
	RunE: func(cmd *cobra.Command, args []string) error {
		return ingestErrors(cmd, os.Stdin, os.Stdout)
	},
}

var ciExecCmd = &cobra.Command{
	Use:   "exec -- command [args...]",
	Short: "Run any CI watcher command and queue matching output lines",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runCIExec,
}

var ciGithubCmd = &cobra.Command{
	Use:   "github",
	Short: "Watch a GitHub Actions run with gh and queue failures",
	RunE:  runCIGithub,
}

const defaultErrorPattern = `(?i)(\berror\b|\bfail(ed|ure)?\b|\bfatal\b|\bexception\b|panic:|::error)`

func runCIGithub(cmd *cobra.Command, args []string) error {
	ghArgs := []string{"run", "watch"}
	if ciRun != "" && ciRun != "latest" {
		ghArgs = append(ghArgs, ciRun)
	}
	ghArgs = append(ghArgs, "--exit-status", "--interval", fmt.Sprintf("%.0f", ciInterval.Seconds()))
	if ciRepo != "" {
		ghArgs = append(ghArgs, "--repo", ciRepo)
	}
	if errorSource == "external" {
		errorSource = "github-actions"
	}
	if errorCategory == "external" {
		errorCategory = "ci"
	}
	if errorDescription == "external error" {
		errorDescription = "GitHub Actions failure"
	}
	return runWatchedCommand(cmd, "gh", ghArgs, os.Stdout)
}

func runCIExec(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("missing command")
	}
	return runWatchedCommand(cmd, args[0], args[1:], os.Stdout)
}

func runWatchedCommand(cmd *cobra.Command, name string, args []string, out io.Writer) error {
	ctx, cancel := signalContext()
	defer cancel()

	c := exec.CommandContext(ctx, name, args...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}

	done := make(chan error, 2)
	go func() { done <- scanAndReport(cmd, stdout, out) }()
	go func() { done <- scanAndReport(cmd, stderr, out) }()
	var scanErr error
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil && scanErr == nil {
			scanErr = err
		}
	}
	waitErr := c.Wait()
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		_ = reportExternalError(cmd, fmt.Sprintf("%s exited with %v", name, waitErr))
		return waitErr
	}
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func ingestErrors(cmd *cobra.Command, in io.Reader, out io.Writer) error {
	return scanAndReport(cmd, in, out)
}

func scanAndReport(cmd *cobra.Command, in io.Reader, out io.Writer) error {
	var re *regexp.Regexp
	if !errorIncludeAll {
		compiled, err := regexp.Compile(errorPattern)
		if err != nil {
			return fmt.Errorf("invalid pattern: %w", err)
		}
		re = compiled
	}

	count := 0
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(out, line)
		if re != nil && !re.MatchString(line) {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := reportExternalError(cmd, line); err != nil {
			return err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "agnt: queued %d error(s)\n", count)
	return nil
}

func reportExternalError(cmd *cobra.Command, line string) error {
	projectPath, err := resolveErrorProjectPath(errorProjectPath)
	if err != nil {
		return err
	}
	payload := protocol.AlertReportPayload{
		PatternID:   "external." + sanitizeQueueID(errorSource),
		Severity:    normalizeSeverity(errorSeverity),
		Category:    errorCategory,
		Description: errorDescription,
		Line:        line,
		ScriptID:    errorSource,
		ProjectPath: projectPath,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
	client := daemon.NewClient(daemon.WithSocketPath(getSocketPath(cmd)))
	if err := client.Connect(); err != nil {
		return err
	}
	defer client.Close()
	return client.AlertReport(payload)
}

func resolveErrorProjectPath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warning", "warn":
		return "warning"
	case "info":
		return "info"
	default:
		return "error"
	}
}

func sanitizeQueueID(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "external"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
