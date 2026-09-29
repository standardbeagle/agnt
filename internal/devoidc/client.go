package devoidc

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client-side helpers shared by the local surfaces that drive the issuer on
// the developer's behalf (the devauth MCP tool and the overlay palette).

// State is the /state response: the caller's current persona and the
// personas it may switch to.
type State struct {
	Persona  string        `json:"persona"`
	Personas []PersonaView `json:"personas"`
}

// PersonaView is one persona as /state reports it.
type PersonaView struct {
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name,omitempty"`
	Roles       []string `json:"roles,omitempty"`
}

// Names lists the persona names in s.
func (s State) Names() []string {
	names := make([]string, 0, len(s.Personas))
	for _, p := range s.Personas {
		names = append(names, p.Name)
	}
	return names
}

// Has reports whether persona is available in s.
func (s State) Has(persona string) bool {
	for _, p := range s.Personas {
		if p.Name == persona {
			return true
		}
	}
	return false
}

var localHTTP = &http.Client{Timeout: 5 * time.Second}

// FetchState reads /state from a proxy's local origin. A proxy without a
// dev-oidc block proxies the prefix to the app, which is reported as "not
// available" with the fix, not as a parse error.
func FetchState(origin string) (State, error) {
	var st State
	resp, err := localHTTP.Get(strings.TrimSuffix(origin, "/") + Prefix + "/state")
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	notAvailable := fmt.Errorf("dev-oidc is not available on this proxy (HTTP %d); declare a dev-oidc block in .agnt.kdl", resp.StatusCode)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return st, notAvailable
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return st, notAvailable
	}
	return st, nil
}

// SwitchScript is the in-page form submit that switches the browser to
// persona: the same POST the indicator makes, so the persona cookie lands in
// the browser and the app is sent back through login. Values are emitted as
// JSON string literals, never spliced code.
func SwitchScript(persona, client string) string {
	p, _ := json.Marshal(persona)
	c, _ := json.Marshal(client)
	return fmt.Sprintf(`(function() {
  var f = document.createElement('form');
  f.method = 'POST';
  f.action = %q;
  var add = function(name, value) {
    if (!value) return;
    var i = document.createElement('input');
    i.type = 'hidden'; i.name = name; i.value = value;
    f.appendChild(i);
  };
  add('persona', %s);
  add('client', %s);
  f.style.display = 'none';
  (document.body || document.documentElement).appendChild(f);
  f.submit();
  return 'switching';
})()`, Prefix+"/switch", p, c)
}

// OriginForProxyURL is the origin a local tool uses to reach a proxy's
// issuer, given the proxy's URL as the daemon reports it:
// http://localhost:<port> for a loopback (or wildcard) listener, where the
// issuer's local checks expect a localhost Host, and the URL's own origin
// otherwise, e.g. https://<MagicDNS name>:<port> for a tailnet proxy serving
// the tailnet certificate.
func OriginForProxyURL(proxyURL string) (string, error) {
	u, err := url.Parse(proxyURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() == "" {
		return "", fmt.Errorf("no proxy URL (%q)", proxyURL)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host == "" || host == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return "http://localhost:" + u.Port(), nil
	}
	return u.Scheme + "://" + u.Host, nil
}

// SchemeMismatches lists the dev-oidc URLs that name this proxy's host with
// the other scheme: an explicit issuer, or a redirect URI, still on http
// after the proxy started serving https (or the reverse). The app signs in
// against those URLs, so each one is a broken login. URLs for other hosts
// (a tunnel hostname, localhost wildcards) are not judged.
func SchemeMismatches(proxyURL, issuer string, redirectURIs []string) []string {
	p, err := url.Parse(proxyURL)
	if err != nil || p.Host == "" {
		return nil
	}
	var out []string
	check := func(kind, raw string) {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Host, p.Host) || u.Scheme == p.Scheme {
			return
		}
		fixed := *u
		fixed.Scheme = p.Scheme
		out = append(out, fmt.Sprintf("%s %s should be %s (the proxy serves %s)", kind, raw, fixed.String(), p.Scheme))
	}
	if issuer != "" {
		check("issuer", issuer)
	}
	for _, r := range redirectURIs {
		check("redirect-uri", r)
	}
	return out
}
