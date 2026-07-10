// Package sshclient implements the client-side SSH transport for
// `agnt ssh <host>[:path]`: ssh_config resolution, auth method chaining,
// host-key verification, ProxyJump multi-hop dialing, a PTY session relay,
// and a keepalive dead-transport detector.
//
// Scope note: this package deliberately does NOT implement port forwarding,
// SFTP, or remote binary bootstrap — those belong to other tasks in the
// remote-ssh epic. It exposes the underlying *ssh.Client publicly (see
// Client.SSH) so those tasks can add direct-tcpip forwards or an SFTP
// subsystem channel without restructuring this package.
package sshclient

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HostConfig is the resolved set of ssh_config directives for one host
// alias, matching the small subset of OpenSSH's ssh_config(5) this package
// supports: HostName, User, Port, IdentityFile (repeatable), ProxyJump.
type HostConfig struct {
	// Host is the alias that was resolved (as passed to Resolve).
	Host string
	// HostName is the actual network name/address to dial. Defaults to
	// Host if not set in config.
	HostName string
	// User is the remote login user. Empty if not resolved from config or
	// the host alias itself (caller may fall back to os/user).
	User string
	// Port defaults to 22 per OpenSSH semantics.
	Port int
	// IdentityFile lists private key paths to try, in file order.
	IdentityFile []string
	// ProxyJump is the raw ProxyJump directive value, e.g.
	// "bastion,jump2:2222" — comma-separated jump hosts, OpenSSH's own
	// multi-hop syntax "[user@]host[:port]" per hop. Empty means no jump.
	ProxyJump string
}

// hostBlock is one "Host <pattern>" section as parsed, prior to resolution
// against a specific alias.
type hostBlock struct {
	patterns  []string
	dirs      map[string]string   // lowercased directive name -> raw value (first wins per OpenSSH semantics for most directives)
	dirsMulti map[string][]string // directives that can repeat (IdentityFile)
}

// ParseSSHConfig reads an OpenSSH-config-format stream and returns the
// parsed host blocks in file order. This is a deliberately small parser:
// it supports "Host <pattern...>" blocks containing HostName, User, Port,
// IdentityFile, and ProxyJump directives. It does NOT support Match blocks,
// Include, or wildcard directive values beyond exact-match Host patterns
// (glob "*" patterns are matched via filepath.Match, which is the one
// nice-to-have beyond pure exact match) — a full ssh_config grammar is out
// of scope per the minimal-code-ladder: this codebase needs a handful of
// directives, not a general-purpose config engine.
func ParseSSHConfig(r io.Reader) ([]hostBlock, error) {
	var blocks []hostBlock
	var cur *hostBlock

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := splitDirective(line)
		if !ok {
			continue
		}
		lowerKey := strings.ToLower(key)
		if lowerKey == "host" {
			if cur != nil {
				blocks = append(blocks, *cur)
			}
			cur = &hostBlock{
				patterns:  strings.Fields(val),
				dirs:      map[string]string{},
				dirsMulti: map[string][]string{},
			}
			continue
		}
		if cur == nil {
			// Directive before any Host block — not supported (no global
			// defaults section in this minimal parser); ignore.
			continue
		}
		switch lowerKey {
		case "identityfile":
			cur.dirsMulti[lowerKey] = append(cur.dirsMulti[lowerKey], expandTilde(val))
		default:
			if _, exists := cur.dirs[lowerKey]; !exists {
				cur.dirs[lowerKey] = val
			}
		}
	}
	if cur != nil {
		blocks = append(blocks, *cur)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sshclient: reading ssh_config: %w", err)
	}
	return blocks, nil
}

// splitDirective splits a "Key value" or "Key=value" ssh_config line into
// its directive name and value.
func splitDirective(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	// ssh_config allows "Key value", "Key   value", or "Key=value".
	idx := strings.IndexAny(line, " \t=")
	if idx < 0 {
		return "", "", false
	}
	key = line[:idx]
	rest := strings.TrimSpace(line[idx:])
	rest = strings.TrimPrefix(rest, "=")
	rest = strings.TrimSpace(rest)
	rest = strings.Trim(rest, `"`)
	return key, rest, true
}

func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// ResolveHost resolves HostConfig for alias by reading the ssh_config file
// at configPath. A missing file resolves to a HostConfig containing only
// defaults (HostName == alias, Port == 22) rather than an error, matching
// OpenSSH behavior of tolerating an absent config file.
func ResolveHost(configPath, alias string) (HostConfig, error) {
	f, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return HostConfig{Host: alias, HostName: alias, Port: 22}, nil
		}
		return HostConfig{}, fmt.Errorf("sshclient: opening ssh_config %s: %w", configPath, err)
	}
	defer f.Close()
	return ResolveHostFromReader(f, alias)
}

// ResolveHostFromReader is ResolveHost taking an io.Reader directly, for
// testability without touching the filesystem.
func ResolveHostFromReader(r io.Reader, alias string) (HostConfig, error) {
	blocks, err := ParseSSHConfig(r)
	if err != nil {
		return HostConfig{}, err
	}

	cfg := HostConfig{Host: alias, Port: 22}
	for _, b := range blocks {
		if !matchesAnyPattern(b.patterns, alias) {
			continue
		}
		// First matching block wins per directive (OpenSSH: first
		// obtained value for each parameter is used); later blocks may
		// still supply directives this one didn't set.
		if cfg.HostName == "" {
			if v, ok := b.dirs["hostname"]; ok {
				cfg.HostName = v
			}
		}
		if cfg.User == "" {
			if v, ok := b.dirs["user"]; ok {
				cfg.User = v
			}
		}
		if cfg.Port == 22 {
			if v, ok := b.dirs["port"]; ok {
				if p, err := strconv.Atoi(v); err == nil {
					cfg.Port = p
				}
			}
		}
		if len(cfg.IdentityFile) == 0 {
			if v, ok := b.dirsMulti["identityfile"]; ok {
				cfg.IdentityFile = append(cfg.IdentityFile, v...)
			}
		}
		if cfg.ProxyJump == "" {
			if v, ok := b.dirs["proxyjump"]; ok {
				cfg.ProxyJump = v
			}
		}
	}
	if cfg.HostName == "" {
		cfg.HostName = alias
	}
	return cfg, nil
}

// matchesAnyPattern reports whether alias matches any of the Host block's
// patterns. Exact match is the guaranteed minimum bar; "*"-style glob
// patterns are supported via filepath.Match as a nice-to-have.
func matchesAnyPattern(patterns []string, alias string) bool {
	for _, p := range patterns {
		if p == alias {
			return true
		}
		if strings.ContainsAny(p, "*?") {
			if matched, err := filepath.Match(p, alias); err == nil && matched {
				return true
			}
		}
	}
	return false
}

// DefaultConfigPath returns the standard ~/.ssh/config path.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// DefaultKnownHostsPath returns the standard ~/.ssh/known_hosts path.
func DefaultKnownHostsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}
