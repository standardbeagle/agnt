package testenv_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/sshclient"
	"github.com/standardbeagle/agnt/internal/sshclient/testenv"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestProxyJumpReachesTargetWithIndependentAuthentication(t *testing.T) {
	jumpAuth, err := testenv.NewAuth("jump-user")
	if err != nil {
		t.Fatal(err)
	}
	targetAuth, err := testenv.NewAuth("target-user")
	if err != nil {
		t.Fatal(err)
	}
	target, err := testenv.Start(targetAuth)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	jumpAddr, stopJump := startJumpServer(t, jumpAuth)
	defer stopJump()

	dir := t.TempDir()
	jumpIdentity := filepath.Join(dir, "id_jump_matrix")
	if err := os.WriteFile(jumpIdentity, jumpAuth.PrivateKey, 0o600); err != nil {
		t.Fatal(err)
	}
	targetIdentity := filepath.Join(dir, "id_target_matrix")
	if err := os.WriteFile(targetIdentity, targetAuth.PrivateKey, 0o600); err != nil {
		t.Fatal(err)
	}
	targetHost, targetPort, err := net.SplitHostPort(target.Addr())
	if err != nil {
		t.Fatal(err)
	}
	jumpHost, jumpPort, err := net.SplitHostPort(jumpAddr)
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("Host jump\n HostName %s\n Port %s\n User %s\n IdentityFile %s\nHost target\n HostName %s\n Port %s\n User %s\n IdentityFile %s\n ProxyJump jump\n",
		jumpHost, jumpPort, jumpAuth.User, jumpIdentity, targetHost, targetPort, targetAuth.User, targetIdentity)
	configPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompts bytes.Buffer
	client, err := sshclient.Dial("target", configPath, filepath.Join(dir, "known_hosts"), targetAuth.User,
		sshclient.Prompter{In: strings.NewReader("yes\nyes\n"), Out: &prompts})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.SSH.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := session.CombinedOutput("printf proxyjump-ok")
	if err != nil || string(out) != "proxyjump-ok" {
		t.Fatalf("target exec = %q, %v", out, err)
	}
	if strings.Count(prompts.String(), "authenticity") != 2 {
		t.Fatalf("host-key prompts = %q, want one per hop", prompts.String())
	}
	known, err := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if err != nil || bytes.Count(known, []byte("ssh-ed25519")) != 2 {
		t.Fatalf("known_hosts = %q, %v", known, err)
	}
}

func TestAuthenticationMatrix(t *testing.T) {
	auth, err := testenv.NewAuth("matrix-user")
	if err != nil {
		t.Fatal(err)
	}
	server, err := testenv.Start(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	identity := filepath.Join(t.TempDir(), "id_matrix")
	if err := os.WriteFile(identity, auth.PrivateKey, 0o600); err != nil {
		t.Fatal(err)
	}

	oldAgent, hadAgent := os.LookupEnv("SSH_AUTH_SOCK")
	t.Cleanup(func() {
		if hadAgent {
			_ = os.Setenv("SSH_AUTH_SOCK", oldAgent)
		} else {
			_ = os.Unsetenv("SSH_AUTH_SOCK")
		}
	})
	_ = os.Unsetenv("SSH_AUTH_SOCK")

	cases := []struct {
		name       string
		identities []string
		agent      bool
		wantOK     bool
	}{
		{name: "identity file", identities: []string{identity}, wantOK: true},
		{name: "ssh agent", agent: true, wantOK: true},
		{name: "unoffered key", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.agent {
				startAgent(t, auth.PrivateKey)
			} else {
				_ = os.Unsetenv("SSH_AUTH_SOCK")
			}
			var prompts bytes.Buffer
			p := sshclient.Prompter{In: strings.NewReader(""), Out: &prompts}
			cfg := &ssh.ClientConfig{
				User:            auth.User,
				Auth:            sshclient.BuildAuthMethods(tc.identities, p),
				HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Host-key policy is asserted separately below.
			}
			client, dialErr := ssh.Dial("tcp", server.Addr(), cfg)
			if (dialErr == nil) != tc.wantOK {
				t.Fatalf("dial error = %v, want success %v", dialErr, tc.wantOK)
			}
			if client != nil {
				defer client.Close()
				session, sessionErr := client.NewSession()
				if sessionErr != nil {
					t.Fatal(sessionErr)
				}
				out, execErr := session.CombinedOutput("printf matrix-ok")
				if execErr != nil || string(out) != "matrix-ok" {
					t.Fatalf("exec = %q, %v", out, execErr)
				}
			}
			if prompts.Len() != 0 {
				t.Fatalf("unexpected interactive prompt: %q", prompts.String())
			}
		})
	}
}

func TestHostKeyMismatchHardFailsWithoutPrompt(t *testing.T) {
	auth, err := testenv.NewAuth("hostkey-user")
	if err != nil {
		t.Fatal(err)
	}
	server, err := testenv.Start(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	wrong, err := testenv.NewAuth("wrong-host-key")
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(server.Addr())}, wrong.PublicKey) + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompt bytes.Buffer
	callback, err := sshclient.HostKeyCallback(knownHosts, sshclient.Prompter{In: strings.NewReader("yes\n"), Out: &prompt})
	if err != nil {
		t.Fatal(err)
	}
	client, dialErr := ssh.Dial("tcp", server.Addr(), &ssh.ClientConfig{
		User: auth.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(auth.Signer)}, HostKeyCallback: callback,
	})
	if client != nil {
		client.Close()
	}
	if dialErr == nil || !strings.Contains(dialErr.Error(), "HOST KEY MISMATCH") {
		t.Fatalf("dial error = %v, want hard host-key mismatch", dialErr)
	}
	if prompt.Len() != 0 {
		t.Fatalf("mismatch prompted user: %q", prompt.String())
	}
	data, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != line {
		t.Fatalf("known_hosts changed on mismatch: %q", data)
	}
}

func startAgent(t *testing.T, privateKey []byte) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	raw, err := ssh.ParseRawPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := keyring.Add(agent.AddedKey{PrivateKey: raw}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn); _ = conn.Close() }()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	if err := os.Setenv("SSH_AUTH_SOCK", sock); err != nil {
		t.Fatal(err)
	}
}

type directTCPIP struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func startJumpServer(t *testing.T, auth *testenv.Auth) (string, func()) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go handleJumpConn(conn, auth.ServerConfig(host))
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close(); <-done }
}

func handleJumpConn(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "direct-tcpip" {
			_ = incoming.Reject(ssh.UnknownChannelType, "direct-tcpip required")
			continue
		}
		var request directTCPIP
		if err := ssh.Unmarshal(incoming.ExtraData(), &request); err != nil {
			_ = incoming.Reject(ssh.ConnectionFailed, "bad destination")
			continue
		}
		upstream, err := net.Dial("tcp", net.JoinHostPort(request.Host, fmt.Sprint(request.Port)))
		if err != nil {
			_ = incoming.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, channelRequests, err := incoming.Accept()
		if err != nil {
			upstream.Close()
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		go func() { _, _ = io.Copy(channel, upstream); _ = channel.Close(); _ = upstream.Close() }()
		go func() { _, _ = io.Copy(upstream, channel); _ = upstream.Close(); _ = channel.Close() }()
	}
}
