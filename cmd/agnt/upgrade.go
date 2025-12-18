package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/updater"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade agnt to the latest version",
	Long: `Upgrade agnt to the latest version.

This command:
  1. Detects how agnt was installed (npm, pip, go, or direct download)
  2. Stops the running daemon gracefully
  3. Updates the package using the appropriate package manager
  4. Restarts the daemon with the new version

Examples:
  agnt upgrade              # Auto-detect and upgrade
  agnt upgrade --check      # Check for updates without installing`,
	Run: runUpgrade,
}

func init() {
	rootCmd.AddCommand(upgradeCmd)

	upgradeCmd.Flags().Bool("check", false, "Check for updates without installing")
	upgradeCmd.Flags().Duration("timeout", 60*time.Second, "Maximum time for upgrade")
}

// installMethod represents how agnt was installed
type installMethod int

const (
	installUnknown installMethod = iota
	installNPM
	installPip
	installGo
	installDirect // curl/PowerShell install or manual download
)

func (m installMethod) String() string {
	switch m {
	case installNPM:
		return "npm"
	case installPip:
		return "pip"
	case installGo:
		return "go"
	case installDirect:
		return "direct"
	default:
		return "unknown"
	}
}

func runUpgrade(cmd *cobra.Command, args []string) {
	checkOnly, _ := cmd.Flags().GetBool("check")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	socketPath := getSocketPath(cmd)

	// Detect installation method
	method, execPath := detectInstallMethod()
	fmt.Printf("Current version: %s\n", appVersion)
	fmt.Printf("Install method: %s\n", method)
	fmt.Printf("Binary path: %s\n", execPath)

	// Check for updates
	fmt.Println("\nChecking for updates...")
	latestVersion, err := checkLatestVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to check for updates: %v\n", err)
		os.Exit(1)
	}

	if latestVersion == appVersion {
		fmt.Printf("Already at latest version (%s)\n", appVersion)
		return
	}

	fmt.Printf("New version available: %s → %s\n", appVersion, latestVersion)

	if checkOnly {
		fmt.Println("\nRun 'agnt upgrade' to install the update.")
		return
	}

	// Stop the daemon first - critical on Windows to avoid EBUSY errors
	fmt.Println("\nStopping daemon...")
	if daemon.IsDaemonRunning(socketPath) {
		if err := daemon.StopDaemon(socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to stop daemon: %v\n", err)
		}
		// Wait for daemon to fully exit with retry
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			if !daemon.IsDaemonRunning(socketPath) {
				break
			}
		}
		// Final check
		if daemon.IsDaemonRunning(socketPath) {
			fmt.Fprintf(os.Stderr, "Error: daemon is still running after stop request.\n")
			fmt.Fprintf(os.Stderr, "Please close any applications using agnt and try again.\n")
			os.Exit(1)
		}
	}

	// Run the appropriate upgrade command
	fmt.Printf("\nUpgrading via %s...\n", method)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := runPackageUpgrade(ctx, method); err != nil {
		errStr := err.Error()
		// Check for common locked file errors
		if strings.Contains(errStr, "EBUSY") ||
			strings.Contains(errStr, "resource busy") ||
			strings.Contains(errStr, "being used by another process") {
			fmt.Fprintf(os.Stderr, "\nUpgrade failed: binary is locked by another process.\n")
			fmt.Fprintf(os.Stderr, "\nThis usually means agnt is still running. Try:\n")
			fmt.Fprintf(os.Stderr, "  1. Close all terminals/applications using agnt\n")
			fmt.Fprintf(os.Stderr, "  2. Run: agnt daemon stop\n")
			fmt.Fprintf(os.Stderr, "  3. Run: agnt upgrade\n")
			if runtime.GOOS == "windows" {
				fmt.Fprintf(os.Stderr, "\nOn Windows, you may also need to:\n")
				fmt.Fprintf(os.Stderr, "  - Close Claude Code or other MCP clients\n")
				fmt.Fprintf(os.Stderr, "  - Kill agnt processes: taskkill /f /im agnt.exe\n")
			}
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Upgrade failed: %v\n", err)
		os.Exit(1)
	}

	// Verify the upgrade
	fmt.Println("\nVerifying upgrade...")
	newVersion, err := getInstalledVersion(execPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not verify new version: %v\n", err)
	} else {
		fmt.Printf("New version installed: %s\n", newVersion)
	}

	fmt.Println("\n✓ Upgrade complete!")
	fmt.Println("The daemon will auto-start on next use.")
}

func detectInstallMethod() (installMethod, string) {
	execPath, err := os.Executable()
	if err != nil {
		return installUnknown, ""
	}
	execPath, _ = filepath.EvalSymlinks(execPath)

	// Check path patterns to determine install method
	pathLower := strings.ToLower(execPath)

	// npm: typically in node_modules or npm global bin
	if strings.Contains(pathLower, "node_modules") ||
		strings.Contains(pathLower, filepath.Join("npm", "bin")) ||
		strings.Contains(pathLower, filepath.Join("fnm", "node-versions")) ||
		strings.Contains(pathLower, filepath.Join("nvm", "versions")) {
		return installNPM, execPath
	}

	// pip: typically in site-packages or Scripts (Windows) or bin (Unix)
	if strings.Contains(pathLower, "site-packages") ||
		strings.Contains(pathLower, filepath.Join("python", "scripts")) ||
		strings.Contains(pathLower, filepath.Join("python3", "bin")) ||
		strings.Contains(pathLower, ".local/bin") {
		// Check if pip knows about agnt
		if _, err := exec.LookPath("pip"); err == nil {
			cmd := exec.Command("pip", "show", "agnt")
			if cmd.Run() == nil {
				return installPip, execPath
			}
		}
	}

	// go: typically in GOPATH/bin or GOBIN
	gopath := os.Getenv("GOPATH")
	gobin := os.Getenv("GOBIN")
	if gopath != "" && strings.HasPrefix(pathLower, strings.ToLower(gopath)) {
		return installGo, execPath
	}
	if gobin != "" && strings.HasPrefix(pathLower, strings.ToLower(gobin)) {
		return installGo, execPath
	}
	if strings.Contains(pathLower, filepath.Join("go", "bin")) {
		return installGo, execPath
	}

	// Default: assume direct install (curl/PowerShell script or manual)
	return installDirect, execPath
}

func checkLatestVersion() (string, error) {
	// Use the updater to check for latest version
	client := daemon.NewClient(daemon.WithSocketPath(daemon.DefaultSocketPath()))
	if err := client.Connect(); err == nil {
		defer client.Close()
		info, err := client.Info()
		if err == nil && info.UpdateInfo != nil && info.UpdateInfo.LatestVersion != "" {
			return info.UpdateInfo.LatestVersion, nil
		}
	}

	// Fallback: query GitHub API directly
	githubChecker := updater.NewGitHubChecker("standardbeagle/agnt")
	release, err := githubChecker.CheckLatestRelease()
	if err != nil {
		return appVersion, fmt.Errorf("failed to check for updates: %w", err)
	}

	return release.GetVersion(), nil
}

func runPackageUpgrade(ctx context.Context, method installMethod) error {
	var cmd *exec.Cmd

	switch method {
	case installNPM:
		cmd = exec.CommandContext(ctx, "npm", "install", "-g", "@standardbeagle/agnt@latest")
	case installPip:
		cmd = exec.CommandContext(ctx, "pip", "install", "--upgrade", "agnt")
	case installGo:
		cmd = exec.CommandContext(ctx, "go", "install", "github.com/standardbeagle/agnt/cmd/agnt@latest")
	case installDirect:
		return runDirectUpgrade(ctx)
	default:
		return fmt.Errorf("unknown install method, please upgrade manually")
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runDirectUpgrade(ctx context.Context) error {
	// For direct installs, use the install script
	if runtime.GOOS == "windows" {
		// PowerShell install
		cmd := exec.CommandContext(ctx, "powershell", "-Command",
			"irm https://raw.githubusercontent.com/standardbeagle/agnt/main/install.ps1 | iex")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Unix: curl install
	cmd := exec.CommandContext(ctx, "bash", "-c",
		"curl -fsSL https://raw.githubusercontent.com/standardbeagle/agnt/main/install.sh | bash")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func getInstalledVersion(execPath string) (string, error) {
	cmd := exec.Command(execPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Parse "agnt vX.Y.Z" format
	version := strings.TrimSpace(string(output))
	version = strings.TrimPrefix(version, "agnt ")
	version = strings.TrimPrefix(version, "v")
	return version, nil
}
