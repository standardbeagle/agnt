package sshclient

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ErrRemoteBinaryMissing wraps a RemoteAgntVersion failure, distinguishing
// "no agnt on PATH remote-side" (the common case bootstrap exists to fix)
// from other session/transport errors. Callers use errors.Is against this
// sentinel rather than string-matching shell output.
var ErrRemoteBinaryMissing = errors.New("sshclient: remote agnt binary not found")

// ErrWindowsRemoteUnsupported is returned by RemoteUname when 'uname -sm'
// fails remote-side — on a real Windows sshd (win32-openssh, PowerShell/
// cmd.exe as the default shell) there is no uname, so its absence is this
// package's only signal. Per the task spec, Windows remotes are an explicit,
// documented v1 gap (named-pipe transport is task 06a), so this is a loud,
// specific failure rather than a generic exec error.
var ErrWindowsRemoteUnsupported = errors.New("sshclient: remote host does not appear to be Linux or macOS (uname unavailable) — Windows remotes are not supported by agnt ssh bootstrap in v1")

// RemoteAgntVersion execs "agnt --version" as a one-shot command on a new
// session channel and parses its version, mirroring the "agnt vX.Y.Z\n"
// shape produced by cmd/agnt/main.go's getVersionString. A non-zero exit
// (command not found, PATH doesn't include it, etc.) is reported as
// ErrRemoteBinaryMissing so callers can distinguish "needs install" from a
// genuine transport failure.
func RemoteAgntVersion(client *ssh.Client) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("sshclient: opening version-check session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run("agnt --version"); err != nil {
		return "", fmt.Errorf("%w: %v (stderr: %s)", ErrRemoteBinaryMissing, err, strings.TrimSpace(stderr.String()))
	}

	return parseAgntVersionOutput(stdout.String())
}

// parseAgntVersionOutput extracts the version token from "agnt vX.Y.Z"
// (see cmd/agnt/main.go:getVersionString), tolerating a trailing daemon
// status line the way daemon/upgrade.go's getNewVersion already does
// (first line only).
func parseAgntVersionOutput(output string) (string, error) {
	line := strings.TrimSpace(strings.SplitN(output, "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", fmt.Errorf("sshclient: unexpected 'agnt --version' output: %q", output)
	}
	return strings.TrimPrefix(fields[len(fields)-1], "v"), nil
}

// RemoteUname execs "uname -sm" and normalizes the result to Go's
// GOOS/GOARCH vocabulary (linux/darwin, amd64/arm64) — v1 supports only
// those two platforms remote-side, per the task spec; anything else
// (including a failed uname, i.e. likely Windows) is ErrWindowsRemoteUnsupported
// or an explicit unsupported-platform error, never a silent guess.
func RemoteUname(client *ssh.Client) (goos, goarch string, err error) {
	session, err := client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("sshclient: opening uname session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run("uname -sm"); err != nil {
		return "", "", fmt.Errorf("%w (uname failed: %v, stderr: %s)", ErrWindowsRemoteUnsupported, err, strings.TrimSpace(stderr.String()))
	}

	fields := strings.Fields(stdout.String())
	if len(fields) != 2 {
		return "", "", fmt.Errorf("sshclient: unexpected 'uname -sm' output: %q", stdout.String())
	}

	goos, err = normalizeRemoteOS(fields[0])
	if err != nil {
		return "", "", err
	}
	goarch, err = normalizeRemoteArch(fields[1])
	if err != nil {
		return "", "", err
	}
	return goos, goarch, nil
}

func normalizeRemoteOS(s string) (string, error) {
	switch strings.ToLower(s) {
	case "linux":
		return "linux", nil
	case "darwin":
		return "darwin", nil
	default:
		return "", fmt.Errorf("sshclient: unsupported remote OS %q (only Linux/macOS supported in v1)", s)
	}
}

func normalizeRemoteArch(s string) (string, error) {
	switch s {
	case "x86_64", "amd64":
		return "amd64", nil
	case "arm64", "aarch64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("sshclient: unsupported remote architecture %q", s)
	}
}

// RemoteHasGo reports whether "go version" succeeds remote-side. Used by
// selectSource to decide between SourceGoInstall and SourceReleaseDownload
// when the local binary can't be copied directly (cross-platform/arch).
func RemoteHasGo(client *ssh.Client) bool {
	session, err := client.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()
	return session.Run("go version") == nil
}

// remoteHome execs "echo $HOME" to resolve the remote user's home
// directory, used to anchor the fixed install location
// ($HOME/.local/bin/agnt, matching install.sh's default AGNT_INSTALL_DIR).
func remoteHome(client *ssh.Client) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("sshclient: opening $HOME session: %w", err)
	}
	defer session.Close()

	var stdout bytes.Buffer
	session.Stdout = &stdout
	if err := session.Run("echo $HOME"); err != nil {
		return "", fmt.Errorf("sshclient: resolving remote $HOME: %w", err)
	}

	home := strings.TrimSpace(stdout.String())
	if home == "" {
		return "", fmt.Errorf("sshclient: remote $HOME is empty")
	}
	return home, nil
}
