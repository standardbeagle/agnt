package sshclient

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
)

// controlSocketDialTimeout bounds how long DialControl and the liveness
// probe in DiscoverActiveHosts will wait for a connection to an existing
// socket file before giving up — a stale socket file left behind by a
// crashed 'agnt ssh' process must not hang callers.
const controlSocketDialTimeout = 2 * time.Second

const (
	controlRequestReadTimeout = 5 * time.Second
	maxControlHeaderBytes     = 64 * 1024
	maxControlPushBytes       = 256 * 1024 * 1024
)

// ErrNoActiveSession is wrapped into the error DialControl and the CLI
// return when no live control socket exists for the requested host — the
// loud, actionable failure required by the daemon-architecture Silent
// Failure Prohibition: a caller of 'agnt push' must be told plainly that no
// 'agnt ssh' session is running, not left to guess from a generic dial
// error.
var ErrNoActiveSession = errors.New("sshclient: no active 'agnt ssh' session")

// controlSocketDir returns ~/.agnt/ssh, creating it (0700) if absent.
func controlSocketDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sshclient: resolving home directory for control socket: %w", err)
	}
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sshclient: creating control socket directory %s: %w", dir, err)
	}
	return dir, nil
}

// ControlSocketPath returns the platform control endpoint registered by
// 'agnt ssh': a Unix socket on Unix/WSL or an owner-only pipe on Windows.
func ControlSocketPath(host string) (string, error) {
	return localControlPath(host)
}

// controlRequestHeader is the JSON-lines header sent over one control
// connection, followed immediately by exactly Size raw bytes for a "push"
// request (Size is 0 and no body follows for "ping").
type controlRequestHeader struct {
	Kind        string `json:"kind"`
	FileName    string `json:"file_name,omitempty"`
	DestRelPath string `json:"dest_rel_path,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// controlResponse is the JSON-lines reply for both "ping" and "push".
type controlResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	RemotePath  string `json:"remote_path,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
}

// ListenControl opens the platform listener. Unix reclaims stale socket files;
// Windows pipe instances vanish with their owner and live collisions fail.
func ListenControl(host string) (net.Listener, error) {
	path, err := ControlSocketPath(host)
	if err != nil {
		return nil, err
	}
	return listenControlTransport(path)
}

// ServeControl accepts connections on ln until it is closed, handling each
// one synchronously in its own goroutine: a "ping" request replies with
// projectRoot (used by DiscoverActiveHosts' liveness probe and by
// PushOneFile to confirm the session is alive); a "push" request streams
// exactly Size bytes and hands them to PushToInbox, replying with the
// resulting remote path or a loud error. sc is the SFTP subsystem opened
// over the same live SSH connection the owning 'agnt ssh' process is
// relaying its PTY through.
func ServeControl(ln net.Listener, projectRoot string, sc *sftp.Client, notifier ...FileArrivalNotifier) {
	var notify FileArrivalNotifier
	if len(notifier) > 0 {
		notify = notifier[0]
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveControlConn(conn, projectRoot, sc, notify)
	}
}

// ServePushQueue is the reconnect-aware control server. Unlike ServeControl,
// its listener stays registered while the SSH transport is down; PushQueue
// decides whether each push executes immediately or waits in its bounded FIFO.
func ServePushQueue(ln net.Listener, queue *PushQueue) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go servePushQueueConn(conn, queue)
	}
}

func servePushQueueConn(conn net.Conn, queue *PushQueue) {
	servePushQueueConnWithLimits(conn, queue, controlRequestReadTimeout, maxControlPushBytes)
}

func servePushQueueConnWithLimits(conn net.Conn, queue *PushQueue, readTimeout time.Duration, maxSize int64) {
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, maxControlHeaderBytes+1)
	header, err := readControlRequest(conn, reader, readTimeout)
	if err != nil {
		writeControlResponse(conn, controlResponse{Error: err.Error()})
		return
	}

	switch header.Kind {
	case "ping":
		writeControlResponse(conn, controlResponse{OK: true, ProjectRoot: queue.ProjectRoot()})
	case "push":
		if err := validateControlPushSize(header.Size, maxSize); err != nil {
			writeControlResponse(conn, controlResponse{Error: err.Error()})
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		remotePath, err := queue.Push(header.FileName, header.DestRelPath, &exactBodyReader{r: reader, remaining: header.Size})
		if err != nil {
			writeControlResponse(conn, controlResponse{Error: err.Error()})
			return
		}
		writeControlResponse(conn, controlResponse{OK: true, RemotePath: remotePath})
	default:
		writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("sshclient: unknown control request kind %q", header.Kind)})
	}
}

func serveControlConn(conn net.Conn, projectRoot string, sc *sftp.Client, notify FileArrivalNotifier) {
	serveControlConnWithLimits(conn, projectRoot, sc, notify, controlRequestReadTimeout, maxControlPushBytes)
}

func serveControlConnWithLimits(conn net.Conn, projectRoot string, sc *sftp.Client, notify FileArrivalNotifier, readTimeout time.Duration, maxSize int64) {
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, maxControlHeaderBytes+1)
	header, err := readControlRequest(conn, reader, readTimeout)
	if err != nil {
		writeControlResponse(conn, controlResponse{Error: err.Error()})
		return
	}

	switch header.Kind {
	case "ping":
		writeControlResponse(conn, controlResponse{OK: true, ProjectRoot: projectRoot})
	case "push":
		if err := validateControlPushSize(header.Size, maxSize); err != nil {
			writeControlResponse(conn, controlResponse{Error: err.Error()})
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		body, err := spoolControlBody(reader, header.Size)
		if err != nil {
			writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("sshclient: reading push body: %v", err)})
			return
		}
		defer func() {
			body.Close()
			os.Remove(body.Name())
		}()
		remotePath, err := PushToInbox(sc, projectRoot, header.DestRelPath, header.FileName, body)
		if err != nil {
			writeControlResponse(conn, controlResponse{Error: err.Error()})
			return
		}
		if notify != nil {
			if err := notify(remotePath, header.Size); err != nil {
				writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("upload completed at %s but agent notice failed: %v", remotePath, err), RemotePath: remotePath})
				return
			}
		}
		writeControlResponse(conn, controlResponse{OK: true, RemotePath: remotePath})
	default:
		writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("sshclient: unknown control request kind %q", header.Kind)})
	}
}

func spoolControlBody(src io.Reader, size int64) (*os.File, error) {
	temp, err := os.CreateTemp("", "agnt-control-push-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		temp.Close()
		os.Remove(temp.Name())
	}
	if _, err := io.CopyN(temp, src, size); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return temp, nil
}

func readControlRequest(conn net.Conn, reader *bufio.Reader, timeout time.Duration) (controlRequestHeader, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := reader.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return controlRequestHeader{}, fmt.Errorf("sshclient: control request header exceeds %d bytes", maxControlHeaderBytes)
		}
		return controlRequestHeader{}, fmt.Errorf("sshclient: reading control request header: %w", err)
	}
	var header controlRequestHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return controlRequestHeader{}, fmt.Errorf("sshclient: malformed control request: %w", err)
	}
	return header, nil
}

func validateControlPushSize(size, maxSize int64) error {
	if size < 0 {
		return errors.New("sshclient: push size must not be negative")
	}
	if size > maxSize {
		return fmt.Errorf("sshclient: push size %d exceeds limit %d", size, maxSize)
	}
	return nil
}

type exactBodyReader struct {
	r         io.Reader
	remaining int64
}

func (r *exactBodyReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func writeControlResponse(conn net.Conn, resp controlResponse) {
	enc, err := json.Marshal(resp)
	if err != nil {
		return
	}
	enc = append(enc, '\n')
	conn.Write(enc)
}

// DialControl connects to host's control socket. Any failure — the socket
// file absent, or present but refusing connections (a stale file from a
// crashed process that ListenControl on the OWNING side hasn't had a
// chance to reclaim yet) — is reported as ErrNoActiveSession, wrapped with
// the underlying cause and the host name, per the Silent Failure
// Prohibition: 'agnt push' must never proceed, or fail with an opaque
// dial error, when there is nothing listening.
func DialControl(host string) (net.Conn, error) {
	path, err := ControlSocketPath(host)
	if err != nil {
		return nil, err
	}
	conn, err := dialControlTransport(path, controlSocketDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w for host %q (socket %s): %w — start 'agnt ssh %s' first", ErrNoActiveSession, host, path, err, host)
	}
	return conn, nil
}

// pingControl sends a "ping" request over conn and returns the decoded
// response, used both by DiscoverActiveHosts (to confirm a socket file is
// actually live, not just present) and available to callers wanting the
// project root before a push.
func pingControl(conn net.Conn) (controlResponse, error) {
	if _, err := conn.Write([]byte(`{"kind":"ping"}` + "\n")); err != nil {
		return controlResponse{}, fmt.Errorf("sshclient: sending ping over control socket: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(controlSocketDialTimeout))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return controlResponse{}, fmt.Errorf("sshclient: reading ping response from control socket: %w", err)
	}
	var resp controlResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return controlResponse{}, fmt.Errorf("sshclient: decoding ping response: %w", err)
	}
	if !resp.OK {
		return controlResponse{}, fmt.Errorf("sshclient: ping rejected: %s", resp.Error)
	}
	return resp, nil
}

// DiscoverActiveHosts lists every host with a live control socket: it globs
// ~/.agnt/ssh/*.ctl and pings each one, keeping only hosts that answer.
// A socket file that exists but does not answer (the owning 'agnt ssh'
// process crashed without cleaning up) is treated as stale and removed —
// mirroring ListenControl's own reclaim-on-register behavior, so a crashed
// session does not linger in discovery results indefinitely between one
// process's exit and another's next 'agnt ssh' to the same host.
func DiscoverActiveHosts() ([]string, error) {
	return discoverControlHosts(controlSocketDialTimeout, pingControl)
}

// PushOneFile implements the client half of the push protocol for a single
// file: dial host's control socket, send a "push" header naming fileName
// and destRelPath, stream size bytes from src, and decode the response.
// Returns the absolute remote path the file was written to.
func PushOneFile(host, fileName, destRelPath string, size int64, src io.Reader) (string, error) {
	if err := validateControlPushSize(size, maxControlPushBytes); err != nil {
		return "", err
	}
	conn, err := DialControl(host)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	header := controlRequestHeader{Kind: "push", FileName: fileName, DestRelPath: destRelPath, Size: size}
	enc, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("sshclient: encoding push request: %w", err)
	}
	enc = append(enc, '\n')
	if _, err := conn.Write(enc); err != nil {
		return "", fmt.Errorf("sshclient: sending push request header: %w", err)
	}
	written, err := io.Copy(conn, io.LimitReader(src, size))
	if err != nil {
		return "", fmt.Errorf("sshclient: streaming %s to %s: %w", fileName, host, err)
	}
	if written != size {
		return "", fmt.Errorf("sshclient: streaming %s to %s: source ended after %d of %d bytes", fileName, host, written, size)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("sshclient: reading push response for %s: %w", fileName, err)
	}
	var resp controlResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return "", fmt.Errorf("sshclient: decoding push response for %s: %w", fileName, err)
	}
	if !resp.OK {
		return "", fmt.Errorf("sshclient: push of %s rejected: %s", fileName, resp.Error)
	}
	return resp.RemotePath, nil
}
