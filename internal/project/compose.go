package project

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// composeVar matches `${VAR}` / `${VAR:-default}` / `${VAR-default}` / `$VAR`
// interpolation in a compose port string.
var composeVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// parseComposePorts returns, per service, the first host-side TCP port the
// service publishes. Short syntax ("127.0.0.1:5173:80", "5173:80",
// "5173", "5173-5175:80-82") and long syntax ({published, target}) are
// both handled. A `${VAR:-default}` resolves to its default; a variable
// with no default cannot be known here and the entry is skipped rather
// than guessed. Services with no published port are absent from the map.
func parseComposePorts(data []byte) (map[string]int, error) {
	var doc struct {
		Services map[string]struct {
			Ports []yaml.Node `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	out := make(map[string]int)
	for name, svc := range doc.Services {
		for _, n := range svc.Ports {
			if port := composeHostPort(&n); port > 0 {
				out[name] = port
				break
			}
		}
	}
	return out, nil
}

func composeHostPort(n *yaml.Node) int {
	switch n.Kind {
	case yaml.ScalarNode:
		return composeShortHostPort(n.Value)
	case yaml.MappingNode:
		var long struct {
			Published string `yaml:"published"`
			Protocol  string `yaml:"protocol"`
		}
		if err := n.Decode(&long); err != nil {
			return 0
		}
		if long.Protocol != "" && long.Protocol != "tcp" {
			return 0
		}
		return firstOfRange(expandComposeVars(long.Published))
	}
	return 0
}

// composeShortHostPort parses the short port syntax and returns the host
// port, or 0 when it is unpublished, a UDP mapping, or unresolvable.
func composeShortHostPort(spec string) int {
	spec = expandComposeVars(strings.TrimSpace(spec))
	if spec == "" {
		return 0
	}
	if i := strings.Index(spec, "/"); i >= 0 {
		if spec[i+1:] != "tcp" {
			return 0
		}
		spec = spec[:i]
	}
	parts := strings.Split(spec, ":")
	var host string
	switch len(parts) {
	case 1:
		// "8080": container port only, docker picks an ephemeral host
		// port — not something a proxy can be pointed at deterministically.
		return 0
	case 2:
		host = parts[0]
	default:
		// "ip:host:container" — IPv6 ip may itself contain colons, so the
		// host port is always the second-to-last segment.
		host = parts[len(parts)-2]
	}
	return firstOfRange(host)
}

// firstOfRange returns the first port of "5173" or "5173-5175".
func firstOfRange(s string) int {
	if i := strings.Index(s, "-"); i >= 0 {
		s = s[:i]
	}
	return atoi(strings.TrimSpace(s))
}

// atoi parses a bare port number; anything non-numeric or above 65535 is 0.
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 0
		}
	}
	return n
}

// expandComposeVars substitutes `${VAR:-default}` with its default and
// erases variables that carry none (the caller then fails to parse a port
// and skips the entry).
func expandComposeVars(s string) string {
	return composeVar.ReplaceAllStringFunc(s, func(m string) string {
		sub := composeVar.FindStringSubmatch(m)
		if sub[2] != "" {
			return sub[2]
		}
		return ""
	})
}
