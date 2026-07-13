//go:build windows

package sshclient

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func localControlPath(host string) (string, error) {
	return windowsPipeName("control", host)
}
func listenControlTransport(path string) (net.Listener, error) {
	if err := validateWindowsPipePath(path); err != nil {
		return nil, err
	}
	return winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: windowsPipeOwnerOnlySDDL, MessageMode: false})
}
func dialControlTransport(path string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return winio.DialPipeContext(ctx, path)
}
func discoverControlHosts(time.Duration, func(net.Conn) (controlResponse, error)) ([]string, error) {
	return nil, fmt.Errorf("sshclient: discovering all active SSH sessions is unsupported on native Windows named pipes; pass --host explicitly")
}
