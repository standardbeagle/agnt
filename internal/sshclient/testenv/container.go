package testenv

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrContainerRuntimeUnavailable means neither Docker nor Podman is usable.
var ErrContainerRuntimeUnavailable = errors.New("docker/podman container runtime unavailable")

// ContainerSSHD is an isolated OpenSSH fixture managed by Docker or Podman.
type ContainerSSHD struct {
	runtime string
	id      string
	addr    string
	auth    *Auth
	tempDir string
}

// StartContainerSSHD launches a containerized sshd and waits until it accepts
// the generated public key. SSH_E2E_IMAGE overrides the fixture image.
func StartContainerSSHD(ctx context.Context) (*ContainerSSHD, error) {
	runtime := ""
	for _, candidate := range []string{"docker", "podman"} {
		path, err := exec.LookPath(candidate)
		if err == nil && exec.CommandContext(ctx, path, "info").Run() == nil {
			runtime = path
			break
		}
	}
	if runtime == "" {
		return nil, ErrContainerRuntimeUnavailable
	}
	user := os.Getenv("SSH_E2E_USER")
	if user == "" {
		user = "tester"
	}
	auth, err := NewAuth(user)
	if err != nil {
		return nil, err
	}
	image := os.Getenv("SSH_E2E_IMAGE")
	if image == "" {
		image = "lscr.io/linuxserver/openssh-server:latest"
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(auth.PublicKey)))
	containerPort := "2222"
	args := []string{"run", "-d", "--rm", "-e", "PUID=1000", "-e", "PGID=1000", "-e", "USER_NAME=" + user, "-e", "PUBLIC_KEY=" + publicKey, "-e", "PASSWORD_ACCESS=false"}
	tempDir := ""
	// SSH_E2E_USER opts into a conventional sshd image rather than the
	// linuxserver.io environment contract. Mounting generated authorized_keys
	// makes locally built fixtures usable offline and keeps credentials unique.
	if os.Getenv("SSH_E2E_USER") != "" {
		containerPort = "22"
		tempDir, err = os.MkdirTemp("", "agnt-sshe2e-")
		if err != nil {
			return nil, fmt.Errorf("create authorized_keys directory: %w", err)
		}
		if err := os.WriteFile(filepath.Join(tempDir, "authorized_keys"), []byte(publicKey+"\n"), 0o600); err != nil {
			_ = os.RemoveAll(tempDir)
			return nil, fmt.Errorf("write authorized_keys: %w", err)
		}
		args = append(args, "-v", tempDir+":/home/"+user+"/.ssh:ro")
	}
	args = append(args, "-p", "127.0.0.1::"+containerPort, image)
	out, err := exec.CommandContext(ctx, runtime, args...).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("start sshd container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fixture := &ContainerSSHD{runtime: runtime, id: strings.TrimSpace(string(out)), auth: auth, tempDir: tempDir}
	cleanup := true
	defer func() {
		if cleanup {
			_ = fixture.Close(context.Background())
		}
	}()
	portOut, err := exec.CommandContext(ctx, runtime, "port", fixture.id, containerPort+"/tcp").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("resolve sshd port: %w: %s", err, strings.TrimSpace(string(portOut)))
	}
	fixture.addr = normalizeContainerPort(strings.TrimSpace(strings.Split(string(portOut), "\n")[0]))
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client, dialErr := ssh.Dial("tcp", fixture.addr, auth.ClientConfig())
		if dialErr == nil {
			_ = client.Close()
			cleanup = false
			return fixture, nil
		}
		lastErr = dialErr
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	logs, _ := exec.CommandContext(ctx, runtime, "logs", fixture.id).CombinedOutput()
	return nil, fmt.Errorf("sshd container not ready: %w; logs(base64)=%s", lastErr, base64.StdEncoding.EncodeToString(logs))
}

func normalizeContainerPort(value string) string {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return value
	}
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Addr returns the host-mapped SSH address.
func (c *ContainerSSHD) Addr() string { return c.addr }

// ClientConfig returns the matching generated client configuration.
func (c *ContainerSSHD) ClientConfig() *ssh.ClientConfig { return c.auth.ClientConfig() }

// Auth returns the generated identity used by the fixture.
func (c *ContainerSSHD) Auth() *Auth { return c.auth }

// Freeze pauses every process in the container without dropping TCP state.
func (c *ContainerSSHD) Freeze(ctx context.Context) error { return c.run(ctx, "pause") }

// Resume resumes a container paused by Freeze.
func (c *ContainerSSHD) Resume(ctx context.Context) error { return c.run(ctx, "unpause") }

// Drop kills the sshd container, deterministically dropping its connections.
func (c *ContainerSSHD) Drop(ctx context.Context) error { return c.run(ctx, "kill") }

// Close removes the fixture if it is still present.
func (c *ContainerSSHD) Close(ctx context.Context) error {
	defer os.RemoveAll(c.tempDir)
	out, err := exec.CommandContext(ctx, c.runtime, "rm", "-f", c.id).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") && !strings.Contains(string(out), "no container with name") {
		return fmt.Errorf("remove sshd container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *ContainerSSHD) run(ctx context.Context, command string) error {
	out, err := exec.CommandContext(ctx, c.runtime, command, c.id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s sshd container: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return nil
}
