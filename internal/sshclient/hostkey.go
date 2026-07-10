package sshclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrHostKeyRejected is returned when the interactive TOFU prompt answers
// "no" to an unknown host key.
var ErrHostKeyRejected = errors.New("sshclient: host key rejected by user")

// Prompter feeds the interactive yes/no host-key prompt from an
// io.Reader (canned answers in tests) and writes the fingerprint prompt
// text to an io.Writer, so no code path in this package requires real
// stdin/stdout to be exercised by tests.
type Prompter struct {
	In  io.Reader
	Out io.Writer
}

// StdioPrompter returns a Prompter wired to the process's real stdin/stdout.
func StdioPrompter() Prompter {
	return Prompter{In: os.Stdin, Out: os.Stdout}
}

// confirmYesNo prints prompt to p.Out and reads a line from p.In,
// returning true only for an affirmative "y"/"yes" answer (case
// insensitive). Any other input, including EOF, is treated as "no".
func (p Prompter) confirmYesNo(prompt string) bool {
	fmt.Fprint(p.Out, prompt)
	reader := bufio.NewReader(p.In)
	line, _ := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// HostKeyCallback builds an ssh.HostKeyCallback that verifies against the
// known_hosts file at knownHostsPath.
//
// Behavior (spec §5.1, the single most security-critical invariant in this
// package):
//   - Key matches a recorded entry → accept silently.
//   - Key error with ZERO `Want` entries (truly unknown host) → prompt
//     interactively via prompter; on "yes", append the key to
//     knownHostsPath and accept for this connection; on anything else,
//     hard fail with ErrHostKeyRejected.
//   - Key error WITH `Want` entries (the host is known but the key
//     changed) → ALWAYS hard fail with a clear error naming the expected
//     vs. actual fingerprint. Never prompt, never fall through, never
//     accept — this is the MITM-detection case.
//
// There is no InsecureIgnoreHostKey path anywhere in this function or
// package.
func HostKeyCallback(knownHostsPath string, prompter Prompter) (ssh.HostKeyCallback, error) {
	// knownhosts.New requires the file to exist; an absent known_hosts is
	// valid (first-ever connection from this machine), so create it empty
	// if missing rather than erroring.
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		if err := os.MkdirAll(dirOf(knownHostsPath), 0o700); err != nil {
			return nil, fmt.Errorf("sshclient: creating known_hosts dir: %w", err)
		}
		f, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("sshclient: creating known_hosts file: %w", err)
		}
		f.Close()
	}

	base, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("sshclient: loading known_hosts %s: %w", knownHostsPath, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := base(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			// Some other error (parse failure, etc.) — do not silently
			// swallow it.
			return err
		}

		if len(keyErr.Want) > 0 {
			// Host is known but presented a DIFFERENT key: possible MITM.
			// Always hard fail, never prompt.
			var expected []string
			for _, w := range keyErr.Want {
				expected = append(expected, ssh.FingerprintSHA256(w.Key))
			}
			return fmt.Errorf(
				"sshclient: HOST KEY MISMATCH for %s: expected one of %v, got %s — refusing connection (possible man-in-the-middle attack)",
				hostname, expected, ssh.FingerprintSHA256(key))
		}

		// Truly unknown host (no existing entries at all): interactive
		// TOFU prompt.
		fp := ssh.FingerprintSHA256(key)
		accepted := prompter.confirmYesNo(fmt.Sprintf(
			"The authenticity of host '%s' can't be established.\n%s key fingerprint is %s.\nAre you sure you want to continue connecting (yes/no)? ",
			hostname, key.Type(), fp))
		if !accepted {
			return ErrHostKeyRejected
		}

		if appendErr := appendKnownHost(knownHostsPath, hostname, key); appendErr != nil {
			return fmt.Errorf("sshclient: accepted host key but failed to persist to %s: %w", knownHostsPath, appendErr)
		}
		return nil
	}, nil
}

// appendKnownHost appends a knownhosts-format line for hostname/key to
// path, using knownhosts.Normalize + knownhosts.Line as the package's own
// canonical formatting helpers (rather than hand-rolling the known_hosts
// line syntax).
func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	normalized := knownhosts.Normalize(hostname)
	line := knownhosts.Line([]string{normalized}, key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		return err
	}
	return nil
}

func dirOf(path string) string {
	idx := strings.LastIndexByte(path, '/')
	if idx < 0 {
		return "."
	}
	return path[:idx]
}
