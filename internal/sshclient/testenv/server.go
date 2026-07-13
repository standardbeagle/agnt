package testenv

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Server is an in-process SSH server listening on a real loopback socket.
type Server struct {
	listener net.Listener
	config   *ssh.ServerConfig

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	frozen chan struct{}
	closed chan struct{}
	once   sync.Once
}

// Start starts an SSH server authenticated by auth. Session exec requests are
// run with the host's /bin/sh, which makes the fixture useful for integration
// tests without requiring an external sshd.
func Start(auth *Auth) (*Server, error) {
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	hostKey, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		return nil, fmt.Errorf("create host signer: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	s := &Server{
		listener: listener,
		config:   auth.ServerConfig(hostKey),
		conns:    make(map[net.Conn]struct{}),
		frozen:   make(chan struct{}),
		closed:   make(chan struct{}),
	}
	close(s.frozen)
	go s.serve()
	return s, nil
}

// Addr returns the server's loopback address.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close stops the listener and every active transport.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		err = s.listener.Close()
		s.Drop()
		close(s.closed)
	})
	return err
}

// Drop closes every currently established TCP connection without stopping the
// listener. New connections can be established immediately afterward.
func (s *Server) Drop() {
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
}

// Freeze prevents the server from answering subsequent SSH requests while
// leaving transports established. Calls are idempotent.
func (s *Server) Freeze() {
	s.mu.Lock()
	select {
	case <-s.frozen:
		s.frozen = make(chan struct{})
	default:
	}
	s.mu.Unlock()
}

// Resume releases requests blocked by Freeze. Calls are idempotent.
func (s *Server) Resume() {
	s.mu.Lock()
	select {
	case <-s.frozen:
	default:
		close(s.frozen)
	}
	s.mu.Unlock()
}

func (s *Server) waitUntilResumed() bool {
	s.mu.Lock()
	resumed := s.frozen
	s.mu.Unlock()
	select {
	case <-resumed:
		return true
	case <-s.closed:
		return false
	}
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go func() {
		for request := range requests {
			if !s.waitUntilResumed() {
				return
			}
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}()
	for newChannel := range channels {
		switch newChannel.ChannelType() {
		case "session":
			channel, channelRequests, err := newChannel.Accept()
			if err == nil {
				go s.handleSession(channel, channelRequests)
			}
		case "direct-tcpip":
			go s.handleDirectTCPIP(newChannel)
		case "direct-streamlocal@openssh.com":
			go s.handleDirectStreamlocal(newChannel)
		default:
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

type directTCPIPRequest struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

type directStreamlocalRequest struct {
	SocketPath string
	Reserved0  string
	Reserved1  uint32
}

func (s *Server) handleDirectTCPIP(newChannel ssh.NewChannel) {
	var request directTCPIPRequest
	if ssh.Unmarshal(newChannel.ExtraData(), &request) != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "invalid direct-tcpip request")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(request.Host, fmt.Sprint(request.Port)))
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	s.acceptRelay(newChannel, target)
}

func (s *Server) handleDirectStreamlocal(newChannel ssh.NewChannel) {
	var request directStreamlocalRequest
	if ssh.Unmarshal(newChannel.ExtraData(), &request) != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "invalid streamlocal request")
		return
	}
	target, err := net.Dial("unix", request.SocketPath)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	s.acceptRelay(newChannel, target)
}

func (s *Server) acceptRelay(newChannel ssh.NewChannel, target net.Conn) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	go func() {
		defer channel.Close()
		defer target.Close()
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(target, channel); done <- struct{}{} }()
		go func() { _, _ = io.Copy(channel, target); done <- struct{}{} }()
		<-done
	}()
}

type execRequest struct{ Command string }

func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		if !s.waitUntilResumed() {
			return
		}
		if request.Type != "exec" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		var payload execRequest
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			continue
		}
		_ = request.Reply(true, nil)
		cmd := exec.Command("/bin/sh", "-c", payload.Command)
		cmd.Stdout = channel
		cmd.Stderr = channel.Stderr()
		err := cmd.Run()
		status := uint32(0)
		if exitErr, ok := err.(*exec.ExitError); ok {
			status = uint32(exitErr.ExitCode())
		} else if err != nil {
			_, _ = io.WriteString(channel.Stderr(), err.Error())
			status = 127
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}
