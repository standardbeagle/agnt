//go:build !windows

package sshclient

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func localControlPath(host string) (string, error) {
	dir, err := controlSocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, host+".ctl"), nil
}
func listenControlTransport(path string) (net.Listener, error) {
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(path)
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
		conn, err := dialControlTransport(path, timeout)
		if err != nil {
			os.Remove(path)
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
