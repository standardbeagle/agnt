package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-cli-server/process"

	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the background daemon",
	Long: `Manage the background daemon that maintains persistent state.

The daemon manages processes and proxies, and survives client disconnections.
It is automatically started when needed, but can be managed manually.`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon in the foreground (blocks until Ctrl+C)",
	Long: `Start the daemon and run it in the FOREGROUND.

This command blocks: it holds the terminal and streams daemon logs until you
press Ctrl+C (or send SIGINT/SIGTERM). It does NOT detach.

Normal use does not need this — MCP tools and 'agnt run' auto-start a detached
daemon on demand. Use 'daemon start' only to run the daemon manually (e.g. to
watch its logs). To manage a running daemon, use 'daemon status' / 'daemon stop'.`,
	Run: runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	Run:   runDaemonStop,
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon",
	Run:   runDaemonRestart,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check daemon status",
	Run:   runDaemonStatus,
}

var daemonInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show daemon information",
	Run:   runDaemonInfo,
}

var daemonCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Kill orphaned test/zombie daemon processes",
	Long: `Find and kill orphaned daemon processes left behind by failed tests
or crashed sessions. Skips the current active daemon.`,
	Run: runDaemonCleanup,
}

var daemonSocketPathCmd = &cobra.Command{
	Use:   "socket-path",
	Short: "Print the daemon's default unix socket path",
	Long: `Print daemonclient.DefaultSocketPath() to stdout, one line, nothing else.

Used by 'agnt ssh' to discover the remote daemon's socket path over an
existing SSH connection (exec'd on the remote side) before opening a
direct-streamlocal forwarding channel to it.`,
	Run: runDaemonSocketPath,
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonInfoCmd)
	daemonCmd.AddCommand(daemonCleanupCmd)
	daemonCmd.AddCommand(daemonSocketPathCmd)
}

func runDaemonSocketPath(cmd *cobra.Command, args []string) {
	fmt.Println(daemonclient.DefaultSocketPath())
}

func getSocketPath(cmd *cobra.Command) string {
	socketPath, _ := cmd.Root().PersistentFlags().GetString("socket")
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}
	return socketPath
}

// newDaemonConfig assembles the DaemonConfig for `daemon start` from the loaded
// app config plus the resolved socket + public-plane addresses. It is a seam so
// the config→daemon threading is unit-testable without driving the blocking
// daemon lifecycle. Notably it is where the operator's feedback{} block (spec
// §5 limits) must reach DaemonConfig.FeedbackLimits — dropping it here leaves the
// live public-plane limiter on defaults regardless of config (no-silent-fallback
// / Config Authority violation).
func newDaemonConfig(appCfg *config.Config, socketPath, publicAddr string) daemon.DaemonConfig {
	// Thread the operator's settings{} block into the live process manager
	// (Config Authority). A non-positive buffer or graceful timeout would
	// disable the bound, so those fall back to the house default; a zero
	// default-timeout is the documented "no timeout".
	settings := appCfg.Settings
	if settings.MaxOutputBuffer <= 0 {
		settings.MaxOutputBuffer = process.DefaultBufferSize
	}
	if settings.GracefulTimeout <= 0 {
		settings.GracefulTimeout = config.DefaultGracefulTimeout
	}
	return daemon.DaemonConfig{
		SocketPath: socketPath,
		ProcessConfig: process.ManagerConfig{
			DefaultTimeout:    settings.DefaultTimeout,
			MaxOutputBuffer:   settings.MaxOutputBuffer,
			GracefulTimeout:   settings.GracefulTimeout,
			HealthCheckPeriod: 10 * time.Second,
		},
		MaxClients:        100,
		WriteTimeout:      30 * time.Second,
		OrphanScanEnabled: true, // production: walk /proc on startup. Tests default to false.
		// production: spawn the detached shim cleanup watcher on SHIM
		// REGISTER / startup sweep. Tests default to false (os.Executable
		// under `go test` is the test binary).
		ShimWatcherEnabled: true,
		// Public walkthrough plane (P7/§2b): opt-in. The token-gated public handler
		// is always BUILT (so `publish feedback` reads work), but a dedicated HTTP
		// listener is only stood up when an operator sets AGNT_PUBLIC_ADDR — we do
		// NOT auto-bind a public port.
		PublicListenAddr: publicAddr,
		// Thread the operator's feedback{} block through to the live limiter.
		// buildPublicPlane Normalize()s this, so a zero/unset field falls back to
		// its spec §5 default while any NON-ZERO operator value survives.
		FeedbackLimits: appCfg.Feedback,
		// Thread the operator's public-plane{} rate block the same way, so the
		// artifact + outbound rate caps honour config rather than staying on
		// defaults regardless (Config Authority).
		PublicPlaneLimits: appCfg.PublicPlane,
	}
}

func runDaemonStart(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	// Create root context with signal cancellation
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Load the app config so operator-set limits (e.g. the feedback{} block)
	// reach the daemon. Fail loud on a malformed config rather than silently
	// falling back to defaults.
	appCfg, err := config.LoadGlobalConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Keys the global config accepts but nothing consumes: say so rather than
	// dropping them silently.
	for _, w := range appCfg.Warnings {
		log.Printf("config warning: %s", w)
	}

	daemonCfg := newDaemonConfig(appCfg, socketPath, os.Getenv("AGNT_PUBLIC_ADDR"))

	d := daemon.New(daemonCfg)

	// When the hub receives a remote SHUTDOWN command, cancel the signal
	// context so this process exits cleanly.  On Windows, Unix signals are
	// not delivered, so without this callback the process would linger
	// after the hub has already torn down its socket.
	d.SetOnShutdown(cancel)

	// Start daemon
	if err := d.Start(); err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}

	log.Printf("Daemon started on %s", socketPath)

	// Wait for shutdown signal or remote SHUTDOWN command
	<-ctx.Done()
	log.Println("Daemon shutdown signal received...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer shutdownCancel()

	if err := d.Stop(shutdownCtx); err != nil {
		log.Printf("Daemon shutdown error: %v", err)
	}

	log.Println("Daemon shutdown complete")
}

// daemonLifecycle holds the seams `daemon stop` and `daemon restart` drive, so
// the stop/verify/start sequencing is testable without a real daemon or real
// sleeps. defaultDaemonLifecycle wires the production implementations.
type daemonLifecycle struct {
	// shutdown connects to the daemon at socketPath and sends SHUTDOWN.
	// connected reports whether a connection was made at all; err is the
	// connect error when !connected, else the SHUTDOWN error.
	shutdown func(socketPath string) (connected bool, err error)
	// isRunning probes the socket: true while something accepts on it.
	isRunning func(socketPath string) bool
	// startDetached starts (or connects to) a detached daemon on socketPath.
	startDetached func(socketPath string) error

	exitTimeout  time.Duration
	pollInterval time.Duration
	now          func() time.Time
	sleep        func(time.Duration)
}

func defaultDaemonLifecycle() daemonLifecycle {
	return daemonLifecycle{
		shutdown:      shutdownDaemonAt,
		isRunning:     daemonclient.IsRunning,
		startDetached: startDetachedDaemon,
		exitTimeout:   5 * time.Second,
		pollInterval:  100 * time.Millisecond,
		now:           time.Now,
		sleep:         time.Sleep,
	}
}

func shutdownDaemonAt(socketPath string) (bool, error) {
	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return false, err
	}
	defer client.Close()
	return true, client.Shutdown()
}

// startDetachedDaemon starts the daemon DETACHED, the way every client
// auto-starts it. Calling runDaemonStart here ran it in this command's
// foreground: restart never returned, and the daemon died with the shell that
// ran it.
func startDetachedDaemon(socketPath string) error {
	config := daemonclient.DefaultAutoStartConfig()
	config.SocketPath = socketPath
	started := daemonclient.NewAutoStartClient(config)
	if err := started.Connect(); err != nil {
		return err
	}
	started.Close()
	return nil
}

// waitForExit polls the socket until nothing accepts on it or exitTimeout
// elapses. It returns true only when the daemon is verifiably gone.
func (l daemonLifecycle) waitForExit(socketPath string) bool {
	deadline := l.now().Add(l.exitTimeout)
	for l.now().Before(deadline) {
		if !l.isRunning(socketPath) {
			return true
		}
		l.sleep(l.pollInterval)
	}
	return false
}

func (l daemonLifecycle) stop(socketPath string, stdout, stderr io.Writer) int {
	connected, err := l.shutdown(socketPath)
	if !connected {
		fmt.Fprintf(stderr, "Daemon is not running: %v\n", err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "Failed to stop daemon: %v\n", err)
		return 1
	}

	// Verify the daemon actually stopped by checking the socket is no longer
	// accepting connections.  This is important on Windows where Unix signals
	// are not delivered and the process could linger.
	if l.waitForExit(socketPath) {
		fmt.Fprintln(stdout, "Daemon stopped")
		return 0
	}

	fmt.Fprintf(stderr, "Error: daemon is still running on %s %s after shutdown was requested; it did not stop\n",
		socketPath, l.exitTimeout)
	return 1
}

func (l daemonLifecycle) restart(socketPath string, stdout, stderr io.Writer) int {
	// Stop the existing daemon, and do not start until it is verifiably gone:
	// the auto-start client connects to whatever answers on the socket, so
	// starting while the old daemon is still dying "restarts" onto it.
	connected, err := l.shutdown(socketPath)
	switch {
	case connected && !l.waitForExit(socketPath):
		if err != nil {
			fmt.Fprintf(stderr, "Error: shutdown request failed (%v) and ", err)
		} else {
			fmt.Fprint(stderr, "Error: ")
		}
		fmt.Fprintf(stderr, "the old daemon is still running on %s %s later; not starting a new one\n",
			socketPath, l.exitTimeout)
		return 1
	case !connected && l.isRunning(socketPath):
		fmt.Fprintf(stderr, "Error: a daemon is answering on %s but could not be reached to stop it (%v); not starting a new one\n",
			socketPath, err)
		return 1
	}

	if err := l.startDetached(socketPath); err != nil {
		fmt.Fprintf(stderr, "Failed to start daemon: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Daemon restarted")
	fmt.Fprintf(stdout, "Socket: %s\n", socketPath)
	return 0
}

func runDaemonStop(cmd *cobra.Command, args []string) {
	if code := defaultDaemonLifecycle().stop(getSocketPath(cmd), os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func runDaemonRestart(cmd *cobra.Command, args []string) {
	if code := defaultDaemonLifecycle().restart(getSocketPath(cmd), os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func runDaemonStatus(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	if daemonclient.IsRunning(socketPath) {
		fmt.Println("Daemon is running")
		fmt.Printf("Socket: %s\n", socketPath)
		os.Exit(0)
	} else {
		fmt.Println("Daemon is not running")
		os.Exit(1)
	}
}

func runDaemonInfo(cmd *cobra.Command, args []string) {
	socketPath := getSocketPath(cmd)

	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Daemon is not running: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	info, err := client.Info()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get daemon info: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Daemon v%s\n", info.Version)
	if info.BuildTime != "" {
		fmt.Printf("Build Time: %s\n", info.BuildTime)
	}
	if info.GitCommit != "" {
		fmt.Printf("Git Commit: %s\n", info.GitCommit)
	}
	fmt.Printf("Socket: %s\n", info.SocketPath)
	fmt.Printf("Uptime: %s\n", info.Uptime.Round(time.Second))
	fmt.Printf("Clients: %d\n", info.ClientCount)
	fmt.Printf("Processes: %d active, %d total, %d failed\n",
		info.ProcessInfo.Active, info.ProcessInfo.TotalStarted, info.ProcessInfo.TotalFailed)
	fmt.Printf("Proxies: %d active, %d total\n",
		info.ProxyInfo.Active, info.ProxyInfo.TotalStarted)

	// Show update notification if available
	if info.UpdateInfo != nil {
		if info.UpdateInfo.Available {
			fmt.Printf("\n🎉 Update available: v%s → v%s\n",
				info.UpdateInfo.CurrentVersion, info.UpdateInfo.LatestVersion)
			fmt.Printf("   Run 'agnt upgrade' to update\n")
			if info.UpdateInfo.ReleaseURL != "" {
				fmt.Printf("   Release notes: %s\n", info.UpdateInfo.ReleaseURL)
			}
		} else if info.UpdateInfo.CheckError != "" {
			fmt.Printf("\n⚠ Update check failed: %s\n", info.UpdateInfo.CheckError)
		} else if !info.UpdateInfo.LastChecked.IsZero() {
			fmt.Printf("\n✓ Up to date (last checked: %s ago)\n",
				time.Since(info.UpdateInfo.LastChecked).Round(time.Second))
		}
	}
}

func runDaemonCleanup(cmd *cobra.Command, args []string) {
	// Find orphaned test daemon processes (socket path contains /tmp/Test)
	out, err := exec.Command("pgrep", "-f", "agnt daemon start --socket /tmp/Test").Output()
	if err != nil {
		fmt.Println("No orphaned test daemons found.")

		// Still check for general zombie daemons via socket library
		socketPath := getSocketPath(cmd)
		cleaned := daemonclient.CleanupZombieDaemons(socketPath)
		if cleaned > 0 {
			fmt.Printf("Cleaned up %d stale socket(s).\n", cleaned)
		}
		return
	}

	var killed int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}

		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}

		_ = p.Signal(syscall.SIGTERM)
		killed++
	}

	if killed > 0 {
		fmt.Printf("Sent SIGTERM to %d orphaned test daemon(s).\n", killed)
		time.Sleep(time.Second)

		// Force kill any survivors
		out2, err := exec.Command("pgrep", "-f", "agnt daemon start --socket /tmp/Test").Output()
		if err == nil {
			var forced int
			for _, line := range strings.Split(strings.TrimSpace(string(out2)), "\n") {
				pid, err := strconv.Atoi(strings.TrimSpace(line))
				if err != nil || pid <= 0 {
					continue
				}
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Signal(syscall.SIGKILL)
					forced++
				}
			}
			if forced > 0 {
				fmt.Printf("Force killed %d remaining daemon(s).\n", forced)
			}
		}
	}

	// Also clean up stale sockets via socket library
	socketPath := getSocketPath(cmd)
	cleaned := daemonclient.CleanupZombieDaemons(socketPath)
	if cleaned > 0 {
		fmt.Printf("Cleaned up %d stale socket(s).\n", cleaned)
	}
}
