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

// ControlSocketPath returns the local control-socket path 'agnt ssh'
// registers for host, and the second-terminal 'agnt push' discovers:
// ~/.agnt/ssh/<host>.ctl.
func ControlSocketPath(host string) (string, error) {
	dir, err := controlSocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, host+".ctl"), nil
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

// ListenControl opens the local unix-socket listener 'agnt ssh' registers
// for host. A pre-existing socket file is treated as stale (the owning
// process is gone or unreachable) and removed before listening: this is
// the normal case after an unclean 'agnt ssh' exit (crash, SIGKILL), and
// failing to reclaim the path would otherwise make every subsequent
// 'agnt ssh <host>' unable to register at all.
func ListenControl(host string) (net.Listener, error) {
	path, err := ControlSocketPath(host)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		os.Remove(path)
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

func serveControlConn(conn net.Conn, projectRoot string, sc *sftp.Client, notify FileArrivalNotifier) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	var header controlRequestHeader
	if err := json.Unmarshal([]byte(line), &header); err != nil {
		writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("sshclient: malformed control request: %v", err)})
		return
	}

	switch header.Kind {
	case "ping":
		writeControlResponse(conn, controlResponse{OK: true, ProjectRoot: projectRoot})
	case "push":
		body := io.LimitReader(reader, header.Size)
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
	conn, err := net.DialTimeout("unix", path, controlSocketDialTimeout)
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
	dir, err := controlSocketDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sshclient: listing control socket directory %s: %w", dir, err)
	}

	var hosts []string
	const suffix = ".ctl"
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) <= len(suffix) || name[len(name)-len(suffix):] != suffix {
			continue
		}
		host := name[:len(name)-len(suffix)]
		path := filepath.Join(dir, name)
		conn, err := net.DialTimeout("unix", path, controlSocketDialTimeout)
		if err != nil {
			os.Remove(path)
			continue
		}
		_, pingErr := pingControl(conn)
		conn.Close()
		if pingErr != nil {
			continue
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// PushOneFile implements the client half of the push protocol for a single
// file: dial host's control socket, send a "push" header naming fileName
// and destRelPath, stream size bytes from src, and decode the response.
// Returns the absolute remote path the file was written to.
func PushOneFile(host, fileName, destRelPath string, size int64, src io.Reader) (string, error) {
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
	if _, err := io.Copy(conn, io.LimitReader(src, size)); err != nil {
		return "", fmt.Errorf("sshclient: streaming %s to %s: %w", fileName, host, err)
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
