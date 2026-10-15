package sshclient

import (
	"bytes"
	"fmt"
	"github.com/standardbeagle/agnt/internal/platform"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/updater"
	"golang.org/x/crypto/ssh"
)

// remoteInstallSubdir is the fixed install location bootstrap uploads/
// installs to, anchored at the remote user's $HOME — matches install.sh's
// default AGNT_INSTALL_DIR ($HOME/.local/bin).
const remoteInstallSubdir = ".local/bin/agnt"

// BootstrapOptions configures CheckRemoteBinary / InstallRemoteBinary.
type BootstrapOptions struct {
	// LocalVersion is this process's own version (main.appVersion). Required.
	LocalVersion string
	// LocalBinaryPath is the path to this process's own executable, used
	// for SourceLocalCopy. Required.
	LocalBinaryPath string
	// LocalGOOS / LocalGOARCH default to runtime.GOOS/runtime.GOARCH when
	// empty — overridable so tests can force a cross-arch/cross-OS
	// decision without cross-compiling a fixture binary.
	LocalGOOS, LocalGOARCH string
	// GitHubRepo defaults to updater.DefaultGitHubRepo when empty.
	GitHubRepo string
}

func (o *BootstrapOptions) applyDefaults() {
	if o.LocalGOOS == "" {
		o.LocalGOOS = runtime.GOOS
	}
	if o.LocalGOARCH == "" {
		o.LocalGOARCH = runtime.GOARCH
	}
	if o.GitHubRepo == "" {
		o.GitHubRepo = updater.DefaultGitHubRepo
	}
}

// BootstrapDecision is the outcome of CheckRemoteBinary: whether an install
// is needed and, if so, how (Source) and where (FinalPath).
type BootstrapDecision struct {
	NeedsInstall  bool
	Reason        string
	Source        Source
	RemoteVersion string // best-effort; empty if the remote had no agnt at all
	RemoteGOOS    string
	RemoteGOARCH  string
	FinalPath     string
}

// CheckRemoteBinary implements the connect-time bootstrap check from the
// task spec: run "agnt --version" remote-side and classify the result as
// missing, outside the compatibility window, or fine. When an install is
// needed it also resolves the remote OS/arch (failing loud on anything but
// Linux/macOS — Windows sshd is an explicit v1 gap) and applies the source
// precedence (selectSource) so the caller knows exactly what InstallRemoteBinary
// will do before committing to it (e.g. to gate on user consent).
func CheckRemoteBinary(client *ssh.Client, opts BootstrapOptions) (BootstrapDecision, error) {
	opts.applyDefaults()

	remoteVersion, verErr := RemoteAgntVersion(client)
	if verErr == nil {
		compatible, cErr := versionsCompatible(opts.LocalVersion, remoteVersion)
		if cErr == nil && compatible {
			return BootstrapDecision{
				NeedsInstall:  false,
				Reason:        fmt.Sprintf("remote agnt %s is within the compatibility window of local %s", remoteVersion, opts.LocalVersion),
				RemoteVersion: remoteVersion,
			}, nil
		}
	}

	remoteGOOS, remoteGOARCH, unameErr := RemoteUname(client)
	if unameErr != nil {
		return BootstrapDecision{}, unameErr
	}

	remoteHasGo := RemoteHasGo(client)
	source := selectSource(opts.LocalGOOS, opts.LocalGOARCH, remoteGOOS, remoteGOARCH, remoteHasGo)

	home, err := remoteHome(client)
	if err != nil {
		return BootstrapDecision{}, err
	}
	finalPath := path.Join(home, remoteInstallSubdir)

	reason := "remote agnt binary was not found"
	if verErr == nil {
		reason = fmt.Sprintf("remote agnt %s is outside the compatibility window of local %s", remoteVersion, opts.LocalVersion)
	}

	return BootstrapDecision{
		NeedsInstall:  true,
		Reason:        reason,
		Source:        source,
		RemoteVersion: remoteVersion,
		RemoteGOOS:    remoteGOOS,
		RemoteGOARCH:  remoteGOARCH,
		FinalPath:     finalPath,
	}, nil
}

// InstallRemoteBinary executes decision.Source's install path. Callers must
// have already gated on consent (--no-bootstrap / --bootstrap=yes / an
// interactive prompt) — this function performs the install unconditionally.
func InstallRemoteBinary(client *ssh.Client, opts BootstrapOptions, decision BootstrapDecision) error {
	opts.applyDefaults()
	if !decision.NeedsInstall {
		return nil
	}

	switch decision.Source {
	case SourceLocalCopy:
		f, err := os.Open(opts.LocalBinaryPath)
		if err != nil {
			return fmt.Errorf("sshclient: opening local binary %s: %w", opts.LocalBinaryPath, err)
		}
		defer f.Close()
		return UploadFile(client, f, decision.FinalPath, 0o755)

	case SourceGoInstall:
		return remoteGoInstall(client, opts.LocalVersion, decision.FinalPath)

	default: // SourceReleaseDownload
		url := ReleaseDownloadURL(opts.GitHubRepo, opts.LocalVersion, decision.RemoteGOOS, decision.RemoteGOARCH)
		return remoteDownloadInstall(client, url, opts.LocalVersion, decision.FinalPath)
	}
}

// ReleaseDownloadURL mirrors install.sh's asset-naming convention
// ("agnt-<platform>-<arch>" under a "vX.Y.Z" release tag) rather than
// reinventing it, so bootstrap and the curl installer always agree on where
// a given release's binaries live.
func ReleaseDownloadURL(repo, version, goos, goarch string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/v%s/agnt-%s-%s",
		repo, strings.TrimPrefix(version, "v"), goos, goarch)
}

// agntModule is the package path 'go install' builds remote-side.
const agntModule = "github.com/standardbeagle/agnt/cmd/agnt"

// releaseVersion returns version without its "v" prefix after checking it
// is a release version that can be pinned: X.Y.Z with an optional
// pre-release/build suffix of [0-9A-Za-z.-+] characters. A dev build's
// version is refused rather than replaced by @latest.
func releaseVersion(version string) (string, error) {
	v := strings.TrimPrefix(version, "v")
	if _, _, _, err := updater.ParseVersion(v); err != nil {
		return "", fmt.Errorf("sshclient: client version %q is not a release version, so the remote install cannot be pinned to it: %w", version, err)
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '+') {
			return "", fmt.Errorf("sshclient: client version %q contains %q, not a release version", version, r)
		}
	}
	return v, nil
}

// remoteGoInstall builds this client's exact version remote-side with
// 'go install <module>@v<version>' (never @latest: the remote must run the
// version the client was released with, and a pinned module version is what
// the Go checksum database authenticates). The binary is built into a
// staging GOBIN beside finalPath, its reported version is checked, and only
// then is it renamed onto finalPath (lessons-ssh-transport.md §6: verify
// before rename-to-activate).
func remoteGoInstall(client *ssh.Client, version, finalPath string) error {
	want, err := releaseVersion(version)
	if err != nil {
		return err
	}
	stageDir := fmt.Sprintf("%s.goinstall.%d", finalPath, time.Now().UnixNano())
	staged := path.Join(stageDir, "agnt")

	cmd := fmt.Sprintf("mkdir -p %s && GOBIN=%s go install %s",
		platform.ShellQuote(stageDir), platform.ShellQuote(stageDir), platform.ShellQuote(agntModule+"@v"+want))
	if err := runRemote(client, cmd); err != nil {
		cleanupRemoteDir(client, stageDir)
		return fmt.Errorf("sshclient: remote go install failed: %w", err)
	}
	if err := verifyRemoteBinaryVersion(client, staged, want); err != nil {
		cleanupRemoteDir(client, stageDir)
		return err
	}
	if err := runRemote(client, fmt.Sprintf("mv %s %s && rmdir %s",
		platform.ShellQuote(staged), platform.ShellQuote(finalPath), platform.ShellQuote(stageDir))); err != nil {
		cleanupRemoteDir(client, stageDir)
		return fmt.Errorf("sshclient: activating go-installed binary: %w", err)
	}
	return nil
}

// remoteDownloadInstall downloads url to a temp name via curl, checks the
// downloaded binary reports the client's version, and only then renames it
// onto finalPath, remote-side.
//
// Integrity gap: the release workflow publishes no checksums, so the bytes
// are trusted on GitHub's TLS alone, as install.sh trusts them. The version
// check catches a wrong or stale asset, not a tampered one. See
// lessons-ssh-transport.md §6.
func remoteDownloadInstall(client *ssh.Client, url, version, finalPath string) error {
	want, err := releaseVersion(version)
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.download.%d", finalPath, time.Now().UnixNano())
	dir := path.Dir(finalPath)

	cmd := fmt.Sprintf("mkdir -p %s && curl -fsSL %s -o %s && chmod 755 %s",
		platform.ShellQuote(dir), platform.ShellQuote(url), platform.ShellQuote(tmpPath), platform.ShellQuote(tmpPath))
	if err := runRemote(client, cmd); err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: remote release download failed: %w", err)
	}
	if err := verifyRemoteBinaryVersion(client, tmpPath, want); err != nil {
		cleanupRemotePath(client, tmpPath)
		return err
	}
	if err := runRemote(client, fmt.Sprintf("mv %s %s", platform.ShellQuote(tmpPath), platform.ShellQuote(finalPath))); err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: activating downloaded binary: %w", err)
	}
	return nil
}

// verifyRemoteBinaryVersion runs '<binPath> --version' remote-side and
// requires it to report exactly want.
func verifyRemoteBinaryVersion(client *ssh.Client, binPath, want string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("sshclient: opening version-verify session: %w", err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Run(platform.ShellQuote(binPath) + " --version"); err != nil {
		return fmt.Errorf("sshclient: installed binary %s did not run: %w (stderr: %s)", binPath, err, strings.TrimSpace(stderr.String()))
	}
	got, err := parseAgntVersionOutput(stdout.String())
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("sshclient: installed binary reports version %s, want %s — not activated", got, want)
	}
	return nil
}

// runRemote runs cmd on a fresh session, folding stderr into the error.
func runRemote(client *ssh.Client, cmd string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	defer session.Close()
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// cleanupRemoteDir best-effort removes a staging directory after a failed
// install; like cleanupRemotePath, the invariant is only that finalPath was
// never replaced.
func cleanupRemoteDir(client *ssh.Client, dir string) {
	_ = runRemote(client, "rm -rf "+platform.ShellQuote(dir))
}
