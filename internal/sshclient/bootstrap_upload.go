package sshclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// UploadFile streams src to remotePath on client, avoiding partial writes
// per the repo-wide "collect modifications, write atomically" standard:
// bytes land at a temp name first, get sha256-verified against what was
// actually sent, and only THEN get chmod'd and renamed onto remotePath (a
// same-filesystem 'mv', atomic on POSIX). If anything fails before the
// rename step — a read error from src, a transport error, or a sha256
// mismatch — remotePath is never created; a best-effort cleanup removes the
// temp file so it doesn't accumulate, but even if that cleanup itself
// fails, the caller's invariant (no partial binary at the final path) still
// holds.
//
// This uses the ssh exec channel (mkdir/cat/sha256sum/chmod/mv over a
// session) rather than SFTP: internal/sshclient has no SFTP client
// dependency, and every remote agnt/install.sh target already ships these
// POSIX coreutils, so adding github.com/pkg/sftp would be new surface for
// no capability gain (see task brief's minimal-code guidance).
func UploadFile(client *ssh.Client, src io.Reader, remotePath string, mode os.FileMode) error {
	tmpPath := fmt.Sprintf("%s.upload.%d.%d", remotePath, os.Getpid(), time.Now().UnixNano())
	remoteDir := path.Dir(remotePath)

	writeSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("sshclient: opening upload session: %w", err)
	}
	defer writeSession.Close()
	stdin, err := writeSession.StdinPipe()
	if err != nil {
		return fmt.Errorf("sshclient: opening upload stdin pipe: %w", err)
	}
	var writeStderr bytes.Buffer
	writeSession.Stderr = &writeStderr

	writeCmd := fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(remoteDir), shellQuote(tmpPath))
	if err := writeSession.Start(writeCmd); err != nil {
		return fmt.Errorf("sshclient: starting remote write: %w", err)
	}

	hasher := sha256.New()
	_, copyErr := io.Copy(stdin, io.TeeReader(src, hasher))
	closeErr := stdin.Close()

	if copyErr != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: reading upload source: %w", copyErr)
	}
	if closeErr != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: closing upload stdin: %w", closeErr)
	}

	if err := writeSession.Wait(); err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: remote write failed: %w (stderr: %s)", err, strings.TrimSpace(writeStderr.String()))
	}

	localSum := hex.EncodeToString(hasher.Sum(nil))
	remoteSum, err := remoteSHA256(client, tmpPath)
	if err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: remote integrity check: %w", err)
	}
	if remoteSum != localSum {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: upload integrity check failed: local sha256 %s != remote %s", localSum, remoteSum)
	}

	// Only past this point — content verified byte-for-byte — do we chmod
	// and atomically rename onto the final path.
	finalizeSession, err := client.NewSession()
	if err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: opening finalize session: %w", err)
	}
	defer finalizeSession.Close()
	var finalizeStderr bytes.Buffer
	finalizeSession.Stderr = &finalizeStderr

	finalizeCmd := fmt.Sprintf("chmod %s %s && mv %s %s",
		modeOctal(mode), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(remotePath))
	if err := finalizeSession.Run(finalizeCmd); err != nil {
		cleanupRemotePath(client, tmpPath)
		return fmt.Errorf("sshclient: finalizing upload (chmod+rename): %w (stderr: %s)", err, strings.TrimSpace(finalizeStderr.String()))
	}
	return nil
}

// remoteSHA256 runs sha256sum on remotePath and returns the hex digest.
func remoteSHA256(client *ssh.Client, remotePath string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Run("sha256sum " + shellQuote(remotePath)); err != nil {
		return "", fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		return "", fmt.Errorf("sshclient: empty sha256sum output for %s", remotePath)
	}
	return fields[0], nil
}

// cleanupRemotePath best-effort removes a leftover temp file after a failed
// upload. Its own failure is not surfaced — the caller's invariant is only
// ever "the FINAL path was never created," not "the temp file is gone."
func cleanupRemotePath(client *ssh.Client, remotePath string) {
	session, err := client.NewSession()
	if err != nil {
		return
	}
	defer session.Close()
	_ = session.Run("rm -f " + shellQuote(remotePath))
}

// modeOctal formats a permission mode as the 3-digit octal string chmod
// expects (e.g. 0o755 -> "755").
func modeOctal(mode os.FileMode) string {
	return strconv.FormatUint(uint64(mode.Perm()), 8)
}
