//go:build windows

package sshclient

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func localControlPath(host string) (string, error) {
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("sshclient: empty host has no control pipe")
	}
	return `\\.\pipe\agnt-ssh-control-` + pipeSafeHost(host), nil
}
func listenControlTransport(path string) (net.Listener, error) {
	if !strings.HasPrefix(strings.ToLower(path), `\\.\pipe\`) {
		return nil, fmt.Errorf("sshclient: native Windows control path must be a named pipe, got %q", path)
	}
	return winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;OW)", MessageMode: false})
}
func dialControlTransport(path string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return winio.DialPipeContext(ctx, path)
}
func discoverControlHosts(time.Duration, func(net.Conn) (controlResponse, error)) ([]string, error) {
	return nil, fmt.Errorf("sshclient: discovering all active SSH sessions is unsupported on native Windows named pipes; pass --host explicitly")
}
