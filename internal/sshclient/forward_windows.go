//go:build windows

package sshclient

import (
	"net"

	"github.com/Microsoft/go-winio"
)

func LocalForwardSocketPath(host string) string {
	path, _ := windowsPipeName("forward", host)
	return path
}

func listenLocalForward(path string) (net.Listener, func() error, error) {
	if err := validateWindowsPipePath(path); err != nil {
		return nil, nil, err
	}
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: windowsPipeOwnerOnlySDDL, MessageMode: false})
	if err != nil {
		return nil, nil, err
	}
	return ln, ln.Close, nil
}
