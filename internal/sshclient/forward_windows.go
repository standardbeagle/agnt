//go:build windows

package sshclient

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
)

func LocalForwardSocketPath(host string) string {
	return `\\.\pipe\agnt-ssh-forward-` + pipeSafeHost(host)
}

func listenLocalForward(path string) (net.Listener, func() error, error) {
	if !strings.HasPrefix(strings.ToLower(path), `\\.\pipe\`) {
		return nil, nil, fmt.Errorf("sshclient: native Windows forward path must be a named pipe, got %q", path)
	}
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;OW)", MessageMode: false})
	if err != nil {
		return nil, nil, err
	}
	return ln, ln.Close, nil
}

func pipeSafeHost(host string) string {
	normalized := strings.ToLower(strings.TrimSpace(host))
	var b strings.Builder
	for _, r := range normalized {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%s-%x", b.String(), sum[:6])
}
