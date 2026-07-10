package sshclient

import (
	"fmt"
	"net"
	"os"
	"strings"

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

// identityFileAuthMethod parses each identity file as a PEM-encoded private
// key. Unencrypted keys parse directly; encrypted keys prompt interactively
// via prompter for the passphrase (never silently skipped). Returns nil if
// no identity files were provided or none could be parsed.
func identityFileAuthMethod(identityFiles []string, prompter Prompter) ssh.AuthMethod {
	var signers []ssh.Signer
	for _, path := range identityFiles {
		signer, err := loadIdentityFile(path, prompter)
		if err != nil {
			fmt.Fprintf(prompter.Out, "sshclient: skipping identity file %s: %v\n", path, err)
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		return nil
	}
	return ssh.PublicKeys(signers...)
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

// passwordFallback is exposed for callers/tests that want a plain
// password AuthMethod without the keyboard-interactive framing (some
// servers only offer "password" auth, not "keyboard-interactive").
func passwordFallback(prompter Prompter) ssh.AuthMethod {
	return ssh.PasswordCallback(func() (string, error) {
		return promptPassphrase(prompter, "password"), nil
	})
}
