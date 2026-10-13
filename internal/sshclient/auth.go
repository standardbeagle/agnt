package sshclient

import (
	"fmt"
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
// methods. Methods are composable — the ssh package itself tries each in
// order and advances only on failure, per its own retry semantics.
func BuildAuthMethods(identityFiles []string, prompter Prompter) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	if am := agentAuthMethod(); am != nil {
		methods = append(methods, am)
	}

	if am := identityFileAuthMethod(identityFiles, prompter); am != nil {
		methods = append(methods, am)
	}

	methods = append(methods, interactiveAuthMethod(prompter))

	return methods
}

// agentAuthMethod connects to SSH_AUTH_SOCK and returns an AuthMethod
// offering all keys the agent holds, or nil if no agent socket is
// configured or reachable.
func agentAuthMethod() ssh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	ag := agent.NewClient(conn)
	return ssh.PublicKeysCallback(ag.Signers)
}

// identityFileAuthMethod offers the keys in identityFiles, loading them only
// when the SSH library asks for them.
//
// The library tries auth methods in order and stops at the first that works,
// so with an agent holding the right key this callback never runs and an
// encrypted key file never prompts. (It used to be parsed up front, on every
// dial and every ProxyJump hop: a passphrase prompt before the agent had been
// tried at all.) Encrypted keys still prompt via prompter when they are
// needed, never silently skipped. Returns nil if no identity files were given.
func identityFileAuthMethod(identityFiles []string, prompter Prompter) ssh.AuthMethod {
	if len(identityFiles) == 0 {
		return nil
	}
	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		var signers []ssh.Signer
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
	identityCache.Store(path, signer)
	return signer, nil
}

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
	if isPassphraseMissing(err, &passphraseErr) {
		passphrase := promptPassphrase(prompter, path)
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("parsing encrypted key with provided passphrase: %w", err)
		}
		return signer, nil
	}
	return nil, err
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
