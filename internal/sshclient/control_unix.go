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
	"syscall"
	"time"
)

const controlSocketProbeTimeout = 250 * time.Millisecond

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
		current, statErr := os.Lstat(path)
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				return nil, fmt.Errorf("sshclient: restating stale control socket %s: %w", path, statErr)
			}
		} else {
			if !os.SameFile(original, current) {
				return nil, fmt.Errorf("sshclient: control socket %s changed during stale probe; refusing to unlink", path)
			}
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("sshclient: removing stale control socket %s: %w", path, err)
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
				if current, currentErr := os.Lstat(path); currentErr == nil && os.SameFile(original, current) {
					_ = os.Remove(path)
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
