package sshclient

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// BuildAuthMethods assembles the []ssh.AuthMethod chain in OpenSSH client
// order: (a) ssh-agent (if SSH_AUTH_SOCK is set and connects), offering all
// its keys; (b) IdentityFile(s) from resolved ssh_config, parsed as PEM,
// prompting interactively for a passphrase when a key is encrypted; (c)
// interactive keyboard-interactive/password fallback if the above yield no
// methods.
//
// Agent keys and identity files are ONE "publickey" method, not two. The ssh
// library tries each method NAME once: after a first publickey method fails it
// skips every later method that is also called publickey. With the agent and
// the identity files as separate methods, a reachable agent that did not hold
// the right key (or held none, which is how macOS's launchd agent starts)
// meant the identity files were never offered at all, and the connection
// failed with "attempted methods [none publickey]".
func BuildAuthMethods(identityFiles []string, prompter Prompter) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	if am := publicKeyAuthMethod(agentSigners(), identityFiles, prompter); am != nil {
		methods = append(methods, am)
	}

	methods = append(methods, interactiveAuthMethod(prompter))

	return methods
}

// agentSigners returns the function listing the keys the ssh-agent at
// SSH_AUTH_SOCK holds, or nil if no agent socket is configured or reachable.
//
// Every dial, and every ProxyJump hop of a dial, builds its auth methods, and
// an agent signer needs its connection until the handshake has used it. One
// connection per build was never closed, so each dial leaked one; the process
// now shares a single agent connection instead (agent clients are safe for
// concurrent use).
func agentSigners() func() ([]ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	if _, err := sharedAgent.get(sock); err != nil {
		return nil
	}
	return func() ([]ssh.Signer, error) { return sharedAgent.signers(sock) }
}

// sharedAgent is the process's one connection to the ssh-agent.
var sharedAgent agentConn

type agentConn struct {
	mu     sync.Mutex
	sock   string
	conn   net.Conn
	client agent.ExtendedAgent
}

// get returns the shared agent client for sock, dialing it if there is none
// or the socket changed.
func (a *agentConn) get(sock string) (agent.ExtendedAgent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil && a.sock == sock {
		return a.client, nil
	}
	if a.conn != nil {
		a.conn.Close()
		a.conn, a.client = nil, nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	a.sock, a.conn, a.client = sock, conn, agent.NewClient(conn)
	return a.client, nil
}

// drop discards client if it is still the shared one, so the next get dials
// afresh (the agent restarted, or the connection broke).
func (a *agentConn) drop(client agent.ExtendedAgent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == client && a.conn != nil {
		a.conn.Close()
		a.conn, a.client = nil, nil
	}
}

// signers lists the agent's keys, redialing once if the shared connection
// has gone bad.
func (a *agentConn) signers(sock string) ([]ssh.Signer, error) {
	client, err := a.get(sock)
	if err != nil {
		return nil, err
	}
	signers, err := client.Signers()
	if err == nil {
		return signers, nil
	}
	a.drop(client)
	if client, err = a.get(sock); err != nil {
		return nil, err
	}
	return client.Signers()
}

// publicKeyAuthMethod offers the agent's keys first, then the identity files,
// as a single publickey method (see BuildAuthMethods). Returns nil when there
// is neither an agent nor an identity file.
//
// The keys are listed only when the library reaches publickey auth, and an
// encrypted identity file is not decrypted even then: it is offered by its
// public half, and the passphrase is asked for only if the server accepts that
// key and a signature is actually needed (lazyIdentity). So an agent that holds
// the right key never causes a passphrase prompt.
func publicKeyAuthMethod(agentKeys func() ([]ssh.Signer, error), identityFiles []string, prompter Prompter) ssh.AuthMethod {
	if agentKeys == nil && len(identityFiles) == 0 {
		return nil
	}
	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		var signers []ssh.Signer
		if agentKeys != nil {
			// An agent that cannot list its keys is not a reason to give up
			// on the identity files.
			if fromAgent, err := agentKeys(); err == nil {
				signers = append(signers, fromAgent...)
			}
		}
		for _, path := range identityFiles {
			signer, err := cachedIdentity(path, prompter)
			if err != nil {
				fmt.Fprintf(prompter.Out, "sshclient: skipping identity file %s: %v\n", path, err)
				continue
			}
			signers = append(signers, signer)
		}
		return signers, nil
	})
}

// identityCache holds the signers already loaded in this process, by path.
// A reconnect dials again with the same identity files; without the cache it
// would ask for the passphrase again on every attempt, from a goroutine
// competing with the session's input pump for stdin. Failed loads are not
// cached, so a mistyped passphrase can be retried.
var identityCache sync.Map // path -> ssh.Signer

func cachedIdentity(path string, prompter Prompter) (ssh.Signer, error) {
	if v, ok := identityCache.Load(path); ok {
		return v.(ssh.Signer), nil
	}
	signer, err := loadIdentityFile(path, prompter)
	if err != nil {
		return nil, err
	}
	// A lazy signer caches itself once it has been unlocked; until then each
	// dial builds a fresh one, so a wrong passphrase is not remembered.
	if _, lazy := signer.(*lazyIdentity); !lazy {
		identityCache.Store(path, signer)
	}
	return signer, nil
}

// loadIdentityFile returns a signer for the key at path. An unencrypted key
// is parsed directly. An encrypted key whose public half is known without the
// passphrase (OpenSSH-format keys carry it in the clear; otherwise a
// path+".pub" file) comes back as a lazyIdentity. Only when the public half
// cannot be found is the passphrase asked for here.
func loadIdentityFile(path string, prompter Prompter) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return signer, nil
	}

	var passphraseErr *ssh.PassphraseMissingError
	if !isPassphraseMissing(err, &passphraseErr) {
		return nil, err
	}
	pub := passphraseErr.PublicKey
	if pub == nil {
		if pubData, readErr := os.ReadFile(path + ".pub"); readErr == nil {
			pub, _, _, _, _ = ssh.ParseAuthorizedKey(pubData)
		}
	}
	if pub == nil {
		return unlockIdentity(path, data, prompter)
	}
	return &lazyIdentity{path: path, data: data, pub: pub, prompter: prompter}, nil
}

// unlockIdentity asks for the passphrase and decrypts the key.
func unlockIdentity(path string, data []byte, prompter Prompter) (ssh.Signer, error) {
	passphrase := promptPassphrase(prompter, path)
	signer, err := ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	if err != nil {
		return nil, fmt.Errorf("parsing encrypted key with provided passphrase: %w", err)
	}
	return signer, nil
}

// lazyIdentity is an encrypted identity file offered by its public key. The
// passphrase prompt happens on the first signature, which the library asks for
// only after the server has said it would accept this key.
type lazyIdentity struct {
	path     string
	data     []byte
	pub      ssh.PublicKey
	prompter Prompter

	mu     sync.Mutex
	signer ssh.Signer
}

func (l *lazyIdentity) PublicKey() ssh.PublicKey { return l.pub }

func (l *lazyIdentity) unlock() (ssh.Signer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.signer != nil {
		return l.signer, nil
	}
	signer, err := unlockIdentity(l.path, l.data, l.prompter)
	if err != nil {
		return nil, err
	}
	l.signer = signer
	identityCache.Store(l.path, signer)
	return signer, nil
}

func (l *lazyIdentity) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	signer, err := l.unlock()
	if err != nil {
		return nil, err
	}
	return signer.Sign(rand, data)
}

// SignWithAlgorithm makes lazyIdentity an ssh.AlgorithmSigner, without which
// the library would restrict an RSA key to the legacy SHA-1 signature.
func (l *lazyIdentity) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	signer, err := l.unlock()
	if err != nil {
		return nil, err
	}
	if as, ok := signer.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(rand, data, algorithm)
	}
	if algorithm != "" && algorithm != l.pub.Type() {
		return nil, fmt.Errorf("sshclient: key %s cannot sign with %s", l.path, algorithm)
	}
	return signer.Sign(rand, data)
}

// isPassphraseMissing checks (via errors.As-equivalent type assertion,
// since ssh.PassphraseMissingError doesn't implement Unwrap in all
// versions) whether err indicates an encrypted private key.
func isPassphraseMissing(err error, target **ssh.PassphraseMissingError) bool {
	if pmErr, ok := err.(*ssh.PassphraseMissingError); ok {
		*target = pmErr
		return true
	}
	return false
}

// promptPassphrase reads a passphrase from the terminal without echoing it,
// using golang.org/x/term when prompter.In is a real terminal (os.Stdin);
// falls back to a plain line read (echoed) for injected io.Reader test
// fixtures, since term.ReadPassword requires a real fd.
func promptPassphrase(prompter Prompter, keyPath string) string {
	fmt.Fprintf(prompter.Out, "Enter passphrase for key '%s': ", keyPath)
	if f, ok := prompter.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		pass, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompter.Out)
		if err == nil {
			return string(pass)
		}
	}
	return strings.TrimRight(readLine(prompter.In), "\r\n")
}

// interactiveAuthMethod builds the keyboard-interactive/password fallback:
// answers every prompt by reading a line from prompter.In without echo
// when possible (falls back to a plain line read for non-terminal
// prompter.In, matching promptPassphrase's approach).
func interactiveAuthMethod(prompter Prompter) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		if name != "" {
			fmt.Fprintln(prompter.Out, name)
		}
		if instruction != "" {
			fmt.Fprintln(prompter.Out, instruction)
		}
		answers := make([]string, len(questions))
		for i, q := range questions {
			fmt.Fprint(prompter.Out, q)
			if f, ok := prompter.In.(*os.File); ok && !echos[i] && term.IsTerminal(int(f.Fd())) {
				pass, err := term.ReadPassword(int(f.Fd()))
				fmt.Fprintln(prompter.Out)
				if err == nil {
					answers[i] = string(pass)
					continue
				}
			}
			answers[i] = strings.TrimRight(readLine(prompter.In), "\r\n")
		}
		return answers, nil
	})
}
