package sshclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/standardbeagle/agnt/internal/platform"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// DefaultInboxDir is the project-relative directory a push lands in when
// the caller does not specify a destination: <project-root>/.agnt-inbox.
const DefaultInboxDir = ".agnt-inbox"

// ErrDestinationExists reports that a no-clobber upload lost the atomic
// final-name claim. Callers may select another name and retry.
var ErrDestinationExists = errors.New("sshclient: push: destination exists")

// maxSymlinkDepth bounds symlink resolution while walking a candidate
// destination's ancestor directories (see checkNoEscapingSymlink), so a
// symlink cycle on the remote host cannot spin this check forever.
const maxSymlinkDepth = 10

// NewSFTPClient opens the SFTP subsystem over an already-established SSH
// connection. This is the sanctioned way to move arbitrary user files
// (task 08a's "agnt push") — unlike UploadFile's bare-exec-channel
// installer (bootstrap_upload.go, task 05), which deliberately avoids an
// SFTP dependency for the single-binary-install use case, bulk/arbitrary
// file push is exactly the case pkg/sftp exists for: directory creation,
// stat/lstat, and rename all need a real protocol rather than hand-rolled
// shell one-liners.
func NewSFTPClient(client *ssh.Client) (*sftp.Client, error) {
	sc, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("sshclient: opening SFTP subsystem: %w", err)
	}
	return sc, nil
}

// PushToInbox uploads src (exactly the bytes of one file) to a path
// computed from projectRoot, destRelPath, and fileName, enforcing the
// traversal guard documented on validateDestRelPath/checkNoEscapingSymlink
// and using the same temp-write -> verify -> rename shape as UploadFile
// (lessons #5/#6 in .claude/rules/lessons-ssh-transport.md): nothing lands
// at the final path until the uploaded bytes have been read back and
// re-hashed, and the destination is re-verified as contained within
// projectRoot immediately before the rename that activates it.
//
// destRelPath == "" defaults to DefaultInboxDir. On success returns the
// absolute remote path the file was written to.
func PushToInbox(sc *sftp.Client, projectRoot, destRelPath, fileName string, src io.Reader) (string, error) {
	return pushToInbox(sc, projectRoot, destRelPath, fileName, src, false)
}

// PushToInboxNoClobber is PushToInbox with an atomic no-overwrite activation.
// It uses the SFTP hardlink extension to claim finalPath: unlike PosixRename,
// hardlink fails when finalPath already exists. The temp and final paths share
// a directory, so successful linking exposes the already-verified inode in one
// operation. Servers without hardlink support return an error rather than
// silently weakening the no-clobber guarantee.
func PushToInboxNoClobber(sc *sftp.Client, projectRoot, destRelPath, fileName string, src io.Reader) (string, error) {
	return pushToInbox(sc, projectRoot, destRelPath, fileName, src, true)
}

func pushToInbox(sc *sftp.Client, projectRoot, destRelPath, fileName string, src io.Reader, noClobber bool) (string, error) {
	if fileName == "" || fileName == "." || fileName == ".." || fileName != path.Base(fileName) {
		return "", fmt.Errorf("sshclient: push: invalid file name %q", fileName)
	}
	if destRelPath == "" {
		destRelPath = DefaultInboxDir
	}
	if err := validateDestRelPath(destRelPath); err != nil {
		return "", err
	}

	projectRoot = path.Clean(projectRoot)
	destDir := path.Join(projectRoot, destRelPath)
	if err := verifyWithinRoot(projectRoot, destDir); err != nil {
		return "", err
	}
	if err := checkNoEscapingSymlink(sc, projectRoot, destDir); err != nil {
		return "", err
	}

	if err := sc.MkdirAll(destDir); err != nil {
		return "", fmt.Errorf("sshclient: push: creating remote directory %s: %w", destDir, err)
	}

	tmpPath := path.Join(destDir, fmt.Sprintf(".%s.push.%d.%d.tmp", fileName, os.Getpid(), time.Now().UnixNano()))
	finalPath := path.Join(destDir, fileName)

	localSum, err := sftpWriteFile(sc, tmpPath, src)
	if err != nil {
		sc.Remove(tmpPath)
		return "", err
	}

	if err := sftpVerifyReadback(sc, tmpPath, localSum); err != nil {
		sc.Remove(tmpPath)
		return "", err
	}

	if err := sc.Chmod(tmpPath, 0o644); err != nil {
		sc.Remove(tmpPath)
		return "", fmt.Errorf("sshclient: push: chmod %s: %w", tmpPath, err)
	}

	// Re-verify containment immediately before the rename that activates
	// the file, rather than trusting the check performed before the write
	// above — see checkNoEscapingSymlink's doc comment for why this
	// narrows (without fully closing) the TOCTOU window.
	if err := checkNoEscapingSymlink(sc, projectRoot, destDir); err != nil {
		sc.Remove(tmpPath)
		return "", err
	}

	if noClobber {
		if err := sc.Link(tmpPath, finalPath); err != nil {
			sc.Remove(tmpPath)
			// Some SFTP servers collapse EEXIST to SSH_FX_FAILURE. Stat the
			// final path after the failed atomic claim so callers can still
			// distinguish a collision from an unsupported/failed hardlink.
			if _, statErr := sc.Lstat(finalPath); statErr == nil {
				return "", fmt.Errorf("%w: %s", ErrDestinationExists, finalPath)
			}
			return "", fmt.Errorf("sshclient: push: claiming %s: %w", finalPath, err)
		}
		sc.Remove(tmpPath)
		return finalPath, nil
	}

	if err := sc.PosixRename(tmpPath, finalPath); err != nil {
		sc.Remove(tmpPath)
		return "", fmt.Errorf("sshclient: push: renaming %s to %s: %w", tmpPath, finalPath, err)
	}
	return finalPath, nil
}

// sftpWriteFile streams src to a fresh remote file at tmpPath, hashing what
// was sent as it goes (via a TeeReader, so the hash reflects exactly the
// bytes handed to the SFTP write, not a separate re-read of src — src is a
// one-shot stream over the control-socket connection in the real caller
// and cannot be rewound). Returns the hex sha256 digest of what was sent,
// for sftpVerifyReadback to compare against what the remote side actually
// stored.
func sftpWriteFile(sc *sftp.Client, tmpPath string, src io.Reader) (string, error) {
	f, err := sc.Create(tmpPath)
	if err != nil {
		return "", fmt.Errorf("sshclient: push: creating remote temp file %s: %w", tmpPath, err)
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(f, io.TeeReader(src, hasher)); err != nil {
		return "", fmt.Errorf("sshclient: push: writing remote temp file %s: %w", tmpPath, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// sftpVerifyReadback re-opens tmpPath over the same SFTP connection, hashes
// what is actually stored there, and compares it against localSum (the
// digest of what sftpWriteFile sent). This is the end-to-end integrity
// check per lessons #5/#6 in .claude/rules/lessons-ssh-transport.md — it
// must run, and must pass, before PushToInbox performs the activating
// rename.
func sftpVerifyReadback(sc *sftp.Client, tmpPath, localSum string) error {
	f, err := sc.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("sshclient: push: reopening remote temp file %s for verify: %w", tmpPath, err)
	}
	defer f.Close()
	remoteHash := sha256.New()
	if _, err := io.Copy(remoteHash, f); err != nil {
		return fmt.Errorf("sshclient: push: reading back remote temp file %s: %w", tmpPath, err)
	}
	remoteSum := hex.EncodeToString(remoteHash.Sum(nil))
	if localSum != remoteSum {
		return fmt.Errorf("sshclient: push: integrity check failed for %s: local sha256 %s != remote %s", tmpPath, localSum, remoteSum)
	}
	return nil
}

// validateDestRelPath rejects a destination path that is, on its face, an
// attempt to leave the project root: an absolute path, or a path whose
// cleaned form starts with (or equals) "..". This is the first of the
// traversal guard's checks — see the package doc comment above PushToInbox
// and checkNoEscapingSymlink for the full algorithm and its TOCTOU
// discussion.
func validateDestRelPath(destRelPath string) error {
	if path.IsAbs(destRelPath) {
		return fmt.Errorf("sshclient: push: destination path %q must be relative to the project root, not absolute", destRelPath)
	}
	cleaned := path.Clean(destRelPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("sshclient: push: destination path %q escapes the project root", destRelPath)
	}
	return nil
}

// verifyWithinRoot confirms that candidate (already produced by
// path.Join(root, ...), so already Clean-equivalent) is root itself or a
// path segment-wise descendant of it. The comparison is segment-safe (it
// checks for root + "/" as a prefix, not a raw string prefix), so a sibling
// directory whose name merely starts with root's name (e.g. root
// "/home/u/proj" vs candidate "/home/u/proj-evil") is correctly rejected.
func verifyWithinRoot(root, candidate string) error {
	root = path.Clean(root)
	candidate = path.Clean(candidate)
	if candidate == root {
		return nil
	}
	if strings.HasPrefix(candidate, root+"/") {
		return nil
	}
	return fmt.Errorf("sshclient: push: resolved destination %q escapes project root %q", candidate, root)
}

// checkNoEscapingSymlink is the traversal guard's third and final check: it
// walks every path segment from root down to dir (inclusive) and, for any
// segment that is itself a symlink, resolves the link target (relative
// targets are resolved against the link's own parent directory, matching
// POSIX semantics) and re-runs verifyWithinRoot against the resolved
// absolute path. This is the check that a plain string comparison of the
// requested path cannot perform: the request never contains "..", but
// root/.agnt-inbox (or any intermediate directory) can itself be a symlink
// planted by an attacker with prior write access to point outside root.
//
// Resolution recurses (a resolved target can itself be a symlink) up to
// maxSymlinkDepth, which also bounds a symlink cycle rather than looping
// forever.
//
// TOCTOU: this check and the eventual write/rename are not perfectly
// atomic — SFTP has no equivalent of openat(2) relative to an already-open,
// symlink-verified directory file descriptor. PushToInbox narrows this gap
// as far as the protocol allows by re-running this exact check immediately
// before the activating PosixRename, rather than trusting the check
// performed before the temp file was written; a symlink swapped in during
// the (typically sub-millisecond) window between that final check and the
// rename syscall itself would still slip through, and closing that
// residual window fully would require cooperation from a remote-side agnt
// process, which is out of scope for this task.
func checkNoEscapingSymlink(sc *sftp.Client, root, dir string) error {
	root = path.Clean(root)
	dir = path.Clean(dir)
	if err := verifyWithinRoot(root, dir); err != nil {
		return err
	}
	if dir == root {
		return nil
	}

	rel := strings.TrimPrefix(dir, root+"/")
	segments := strings.Split(rel, "/")
	current := root
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		current = path.Join(current, seg)
		if err := resolveSymlinkChain(sc, root, current, 0); err != nil {
			return err
		}
	}
	return nil
}

// resolveSymlinkChain Lstats p; if p is not a symlink, it is left as-is (its
// ancestors were already validated by the caller's loop and by construction
// p == path.Join(root, ...) at this point, so it needs no further check
// here). If p IS a symlink, its target is resolved and verified within
// root, then recursively checked in case the target is itself a symlink.
func resolveSymlinkChain(sc *sftp.Client, root, p string, depth int) error {
	if depth > maxSymlinkDepth {
		return fmt.Errorf("sshclient: push: symlink chain under %q exceeds depth %d resolving %q", root, maxSymlinkDepth, p)
	}
	info, err := sc.Lstat(p)
	if err != nil {
		// A missing intermediate directory is not a traversal attempt —
		// PushToInbox's MkdirAll (or a later component's own Lstat) is
		// where that surfaces as a real error; here it is simply not a
		// symlink to resolve.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sshclient: push: lstat %q: %w", p, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}

	target, err := sc.ReadLink(p)
	if err != nil {
		return fmt.Errorf("sshclient: push: reading symlink target of %q: %w", p, err)
	}
	resolved := target
	if !path.IsAbs(resolved) {
		resolved = path.Join(path.Dir(p), resolved)
	}
	resolved = path.Clean(resolved)
	if err := verifyWithinRoot(root, resolved); err != nil {
		return fmt.Errorf("sshclient: push: symlink %q resolves outside project root: %w", p, err)
	}
	return resolveSymlinkChain(sc, root, resolved, depth+1)
}

// ResolveRemoteProjectRoot resolves remotePath (as given to 'agnt ssh
// host:remotePath', possibly relative or empty) to an absolute path on the
// remote host by running a one-shot 'cd ... && pwd' over a fresh session
// channel: empty remotePath 'cd's to the remote user's home directory
// (POSIX 'cd' with no argument), matching the default project root an
// unqualified 'agnt ssh host' connects to.
func ResolveRemoteProjectRoot(client *ssh.Client, remotePath string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("sshclient: opening session to resolve remote project root: %w", err)
	}
	defer session.Close()

	var cmd string
	if remotePath == "" {
		cmd = "cd && pwd"
	} else {
		cmd = "cd " + platform.ShellQuote(remotePath) + " && pwd"
	}

	out, err := session.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("sshclient: resolving remote project root (%q): %w", cmd, err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" || !path.IsAbs(root) {
		return "", fmt.Errorf("sshclient: resolving remote project root: unexpected output %q", string(out))
	}
	return root, nil
}
