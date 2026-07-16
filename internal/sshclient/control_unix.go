//go:build !windows

package sshclient

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

const controlSocketProbeTimeout = 250 * time.Millisecond

type controlUnixHook struct{ run func(stage, path string) }

var controlUnixTestHook atomic.Pointer[controlUnixHook]

func runControlUnixHook(stage, path string) {
	if hook := controlUnixTestHook.Load(); hook != nil {
		hook.run(stage, path)
	}
}

func localControlPath(host string) (string, error) {
	dir, err := controlSocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, host+".ctl"), nil
}
func listenControlTransport(path string) (net.Listener, error) {
	if original, err := os.Lstat(path); err == nil {
		if original.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("sshclient: refusing to replace non-socket control path %s", path)
		}
		live, probeErr := probeControlSocket(path, controlSocketProbeTimeout)
		if live {
			return nil, fmt.Errorf("sshclient: control socket %s is already connected to a live session", path)
		}
		if !isConclusiveStaleControlError(probeErr) {
			return nil, fmt.Errorf("sshclient: refusing to reclaim control socket %s after inconclusive liveness probe: %w", path, probeErr)
		}
		runControlUnixHook("listen-after-probe", path)
		quarantine, claimErr := claimStaleControlPath(path, original)
		if claimErr != nil {
			return nil, claimErr
		}
		if quarantine != "" {
			runControlUnixHook("listen-before-delete", path)
			if err := os.Remove(quarantine); err != nil {
				return nil, fmt.Errorf("sshclient: removing quarantined stale control socket %s: %w", quarantine, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("sshclient: inspecting control socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sshclient: listening on control socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, fmt.Errorf("sshclient: securing control socket %s: %w", path, err)
	}
	return ln, nil
}

func claimStaleControlPath(path string, probed os.FileInfo) (string, error) {
	quarantine := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.quarantine.%d.%d", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	if err := os.Rename(path, quarantine); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("sshclient: quarantining stale control socket %s: %w", path, err)
	}
	runControlUnixHook("after-quarantine", path)
	claimed, err := os.Lstat(quarantine)
	if err != nil {
		return "", fmt.Errorf("sshclient: inspecting quarantined control socket %s: %w", quarantine, err)
	}
	if sameControlFile(probed, claimed) {
		return quarantine, nil
	}
	if err := restoreQuarantinedControlPath(quarantine, path); err != nil {
		return "", fmt.Errorf("sshclient: control socket %s changed during stale claim and could not be restored: %w", path, err)
	}
	return "", fmt.Errorf("sshclient: control socket %s changed during stale claim; replacement preserved", path)
}

func sameControlFile(a, b os.FileInfo) bool {
	// SameFile alone is vulnerable to immediate inode reuse after an owner
	// unlinks and replaces the socket. Creation metadata makes that reuse fail
	// closed while retaining a portable Unix implementation.
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func restoreQuarantinedControlPath(quarantine, path string) error {
	// Link is an atomic no-replace claim: unlike Rename it cannot overwrite a
	// newer owner that appeared at path while the quarantined inode was checked.
	if err := os.Link(quarantine, path); err != nil {
		return err
	}
	return os.Remove(quarantine)
}

func probeControlSocket(path string, timeout time.Duration) (bool, error) {
	conn, err := dialControlTransport(path, timeout)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(`{"kind":"ping"}` + "\n")); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return false, err
	}
	var response controlResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return false, err
	}
	if !response.OK {
		return false, fmt.Errorf("control ping rejected: %s", response.Error)
	}
	return true, nil
}

func isConclusiveStaleControlError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}
func dialControlTransport(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}
func discoverControlHosts(timeout time.Duration, ping func(net.Conn) (controlResponse, error)) ([]string, error) {
	dir, err := controlSocketDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sshclient: listing control socket directory %s: %w", dir, err)
	}
	var hosts []string
	for _, entry := range entries {
		name := entry.Name()
		const suffix = ".ctl"
		if entry.IsDir() || len(name) <= len(suffix) || name[len(name)-len(suffix):] != suffix {
			continue
		}
		host := name[:len(name)-len(suffix)]
		path := filepath.Join(dir, name)
		original, statErr := os.Lstat(path)
		if statErr != nil {
			continue
		}
		conn, err := dialControlTransport(path, timeout)
		if err != nil {
			if original.Mode()&os.ModeSocket != 0 && isConclusiveStaleControlError(err) {
				runControlUnixHook("discover-after-probe", path)
				if quarantine, claimErr := claimStaleControlPath(path, original); claimErr == nil && quarantine != "" {
					runControlUnixHook("discover-before-delete", path)
					_ = os.Remove(quarantine)
				}
			}
			continue
		}
		_, pingErr := ping(conn)
		conn.Close()
		if pingErr == nil {
			hosts = append(hosts, host)
		}
	}
	return hosts, nil
}
