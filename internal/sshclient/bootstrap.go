package sshclient

import (
	"bytes"
	"fmt"
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
		return remoteGoInstall(client, decision.FinalPath)

	default: // SourceReleaseDownload
		url := ReleaseDownloadURL(opts.GitHubRepo, opts.LocalVersion, decision.RemoteGOOS, decision.RemoteGOARCH)
		return remoteDownloadInstall(client, url, decision.FinalPath)
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

// remoteGoInstall runs 'go install .../cmd/agnt@latest' remote-side with
// GOBIN pointed at finalPath's directory. Go's own install step already
// writes to a temp file in GOBIN and renames (its build cache/output
// discipline), so this does not duplicate UploadFile's manual
// temp+sha256+rename dance — that atomicity is Go toolchain's job here, not
// ours.
func remoteGoInstall(client *ssh.Client, finalPath string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("sshclient: opening go-install session: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stderr = &stderr

	dir := path.Dir(finalPath)
	cmd := fmt.Sprintf("mkdir -p %s && GOBIN=%s go install github.com/standardbeagle/agnt/cmd/agnt@latest",
		shellQuote(dir), shellQuote(dir))
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("sshclient: remote go install failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// remoteDownloadInstall downloads url to a temp name via curl and only then
// chmod+renames onto finalPath, remote-side, in a single shell invocation —
// same avoid-partial-writes discipline as UploadFile, just curl-driven
// instead of exec-channel-piped since the bytes are already on GitHub, not
// local to this process.
func remoteDownloadInstall(client *ssh.Client, url, finalPath string) error {
	tmpPath := fmt.Sprintf("%s.download.%d", finalPath, time.Now().UnixNano())
	dir := path.Dir(finalPath)

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("sshclient: opening download-install session: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf("mkdir -p %s && curl -fsSL %s -o %s && chmod 755 %s && mv %s %s",
		shellQuote(dir), shellQuote(url), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(finalPath))
	if err := session.Run(cmd); err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: remote release download failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
