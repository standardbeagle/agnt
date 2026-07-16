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
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
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
		quarantine, claimErr := quarantineStaleControlPath(path, controlSocketProbeTimeout)
		if claimErr != nil {
			return nil, claimErr
		}
		if quarantine != "" {
			runControlUnixHook("listen-before-delete", path)
			if err := removeControlQuarantine(quarantine); err != nil {
				return nil, fmt.Errorf("sshclient: removing quarantined stale control socket %s: %w", quarantine, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("sshclient: inspecting control socket %s: %w", path, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("sshclient: listening on control socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.SetUnlinkOnClose(false)
		ln.Close()
		if quarantine, cleanupErr := quarantineStaleControlPath(path, controlSocketProbeTimeout); cleanupErr == nil && quarantine != "" {
			_ = removeControlQuarantine(quarantine)
		}
		return nil, fmt.Errorf("sshclient: securing control socket %s: %w", path, err)
	}
	ln.SetUnlinkOnClose(false)
	return &ownedControlListener{UnixListener: ln, path: path}, nil
}

type ownedControlListener struct {
	*net.UnixListener
	path      string
	closeOnce atomic.Bool
}

func (l *ownedControlListener) Close() error {
	if !l.closeOnce.CompareAndSwap(false, true) {
		return net.ErrClosed
	}
	closeErr := l.UnixListener.Close()
	quarantine, cleanupErr := quarantineStaleControlPath(l.path, controlSocketProbeTimeout)
	if cleanupErr == nil && quarantine != "" {
		cleanupErr = removeControlQuarantine(quarantine)
	}
	return errors.Join(closeErr, cleanupErr)
}

func quarantineStaleControlPath(path string, timeout time.Duration) (string, error) {
	quarantineDir, err := os.MkdirTemp(filepath.Dir(path), ".q")
	if err != nil {
		return "", fmt.Errorf("sshclient: reserving control quarantine beside %s: %w", path, err)
	}
	quarantine := filepath.Join(quarantineDir, "s")
	if err := os.Rename(path, quarantine); err != nil {
		_ = os.Remove(quarantineDir)
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("sshclient: quarantining stale control socket %s: %w", path, err)
	}
	runControlUnixHook("after-quarantine", path)
	// Re-link without overwrite so the quarantined endpoint can be probed at
	// its canonical bound pathname. Keeping the quarantine hard link pins the
	// exact inode while path is probed and, if stale, unlinked.
	if err := os.Link(quarantine, path); err != nil {
		return "", fmt.Errorf("sshclient: quarantined endpoint %s could not be claimed at %s without overwriting a replacement: %w", quarantine, path, err)
	}
	live, probeErr := probeControlSocket(path, timeout)
	if live || !isConclusiveStaleControlError(probeErr) {
		if err := removeControlQuarantine(quarantine); err != nil {
			return "", fmt.Errorf("sshclient: preserving inconclusive control endpoint at %s but removing quarantine %s failed: %w", path, quarantine, err)
		}
		if live {
			return "", fmt.Errorf("sshclient: live control endpoint changed during stale claim; replacement preserved")
		}
		return "", fmt.Errorf("sshclient: control endpoint changed during stale claim and probe was inconclusive: %w", probeErr)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("sshclient: unlinking claimed stale control path %s: %w", path, err)
	}
	return quarantine, nil
}

func removeControlQuarantine(quarantine string) error {
	if err := os.Remove(quarantine); err != nil {
		return err
	}
	return os.Remove(filepath.Dir(quarantine))
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
	var cleanupErrors []error
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
				quarantine, claimErr := quarantineStaleControlPath(path, timeout)
				if claimErr != nil {
					cleanupErrors = append(cleanupErrors, claimErr)
				} else if quarantine != "" {
					runControlUnixHook("discover-before-delete", path)
					if removeErr := removeControlQuarantine(quarantine); removeErr != nil {
						cleanupErrors = append(cleanupErrors, fmt.Errorf("sshclient: removing quarantined stale socket %s: %w", quarantine, removeErr))
					}
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
	return hosts, errors.Join(cleanupErrors...)
}
