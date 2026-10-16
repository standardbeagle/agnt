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
	"github.com/standardbeagle/agnt/internal/daemonclient"
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
	upgradeCmd.Flags().Duration("timeout", defaultUpgradeTimeout, "Maximum time for the install step (it is killed past this)")
}

// defaultUpgradeTimeout bounds the package-manager / install-script step. It
// is a backstop against a hung installer, not a performance target: the old
// 60s default killed a cold-cache `go install` or a slow `npm install -g`
// partway through, with the daemon already stopped.
const defaultUpgradeTimeout = 10 * time.Minute

// versionIsNewer reports whether latest is a strictly higher version than
// current, prereleases included (1.0.0 is newer than 1.0.0-rc.1), by the same
// rule as the daemon's update notice. An unparsable version is an error, never
// a guess.
func versionIsNewer(latest, current string) (bool, error) {
	if _, _, _, err := updater.ParseVersion(latest); err != nil {
		return false, fmt.Errorf("latest version %q: %w", latest, err)
	}
	if _, _, _, err := updater.ParseVersion(current); err != nil {
		return false, fmt.Errorf("installed version %q: %w", current, err)
	}
	return updater.IsNewerVersion(latest, current)
}

// upgradeTools lists the executables the install step for method needs.
func upgradeTools(method installMethod, goos string) ([]string, error) {
	switch method {
	case installNPM:
		return []string{"npm"}, nil
	case installPip:
		return []string{"pip"}, nil
	case installGo:
		return []string{"go"}, nil
	case installDirect:
		if goos == "windows" {
			return []string{"powershell"}, nil
		}
		return []string{"bash", "curl"}, nil
	default:
		return nil, fmt.Errorf("unknown install method, please upgrade manually")
	}
}

// checkUpgradeRunnable verifies every tool the install step will exec is on
// PATH. lookPath is injected for tests.
func checkUpgradeRunnable(method installMethod, lookPath func(string) (string, error)) error {
	tools, err := upgradeTools(method, runtime.GOOS)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		if _, err := lookPath(tool); err != nil {
			return fmt.Errorf("the %s install method needs %q, which was not found on PATH", method, tool)
		}
	}
	return nil
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

	// Compare semantically. String inequality used to be enough to "upgrade",
	// which reinstalled the published release over a newer local build.
	newer, err := versionIsNewer(latestVersion, appVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot compare the installed version (%s) with the latest release (%s): %v\n", appVersion, latestVersion, err)
		fmt.Fprintf(os.Stderr, "Not upgrading, since that could replace a newer build with an older one. Reinstall manually via %s if you want the published release.\n", method)
		os.Exit(1)
	}
	if !newer {
		if latestVersion == appVersion {
			fmt.Printf("Already at latest version (%s)\n", appVersion)
		} else {
			fmt.Printf("Installed version %s is newer than the latest release (%s); nothing to do.\n", appVersion, latestVersion)
		}
		return
	}

	fmt.Printf("New version available: %s → %s\n", appVersion, latestVersion)

	if checkOnly {
		fmt.Println("\nRun 'agnt upgrade' to install the update.")
		return
	}

	// Confirm the install step can run at all before touching the daemon.
	// Stopping it first and then discovering there is no npm/pip/go on PATH
	// left the user with their processes and proxies down and nothing upgraded.
	if err := checkUpgradeRunnable(method, exec.LookPath); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot upgrade: %v\n", err)
		fmt.Fprintf(os.Stderr, "The daemon was left running; nothing was changed.\n")
		os.Exit(1)
	}

	// Stop the daemon first - critical on Windows to avoid EBUSY errors
	fmt.Println("\nStopping daemon...")
	if daemonclient.IsDaemonRunning(socketPath) {
		if err := daemonclient.StopDaemon(socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to stop daemon: %v\n", err)
		}
		// Wait for daemon to fully exit with retry
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			if !daemonclient.IsDaemonRunning(socketPath) {
				break
			}
		}
		// Final check
		if daemonclient.IsDaemonRunning(socketPath) {
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
	// Resolve symlinks when possible, but keep the original path on failure —
	// blanking execPath would silently misclassify the install method.
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}

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
	client := daemonclient.NewClient(daemonclient.WithSocketPath(daemonclient.DefaultSocketPath()))
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
