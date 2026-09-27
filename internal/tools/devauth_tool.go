package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/devoidc"
	"github.com/standardbeagle/go-sdk/mcp"
)

// DevAuthInput is the input for the devauth tool.
type DevAuthInput struct {
	Action  string `json:"action" jsonschema:"Action: personas, as, token"`
	ProxyID string `json:"proxy_id" jsonschema:"Proxy whose dev-oidc issuer to use"`
	Persona string `json:"persona,omitempty" jsonschema:"Persona name (required for as and token)"`
	Client  string `json:"client,omitempty" jsonschema:"Client id: for token, the access token's client (required); for as, whose login-path to land on"`
}

// DevAuthPersona is one persona as the issuer reports it.
type DevAuthPersona struct {
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name,omitempty"`
	Roles       []string `json:"roles,omitempty"`
}

// DevAuthOutput is the output of the devauth tool.
type DevAuthOutput struct {
	Issuer      string           `json:"issuer,omitempty"`
	Current     string           `json:"current,omitempty"`
	Personas    []DevAuthPersona `json:"personas,omitempty"`
	AccessToken string           `json:"access_token,omitempty"`
	ExpiresIn   int              `json:"expires_in,omitempty"`
	Message     string           `json:"message,omitempty"`
}

// RegisterDevAuthTool registers the devauth MCP tool.
func RegisterDevAuthTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "devauth",
		Description: `Sign in to the app under development as a dev-oidc persona.

Needs a dev-oidc block in .agnt.kdl (see docs/configuration.md § Dev OIDC).
agnt serves a dev OIDC issuer at <proxy>/__agnt/oidc; personas are the users
declared there.

Actions:
  personas: list the personas and the issuer URL
  as:       switch the browser behind proxy_id to a persona; the app is sent
            to its login-path and signs in again as that persona
  token:    mint an access token for persona + client, for API calls

Examples:
  devauth {action: "personas", proxy_id: "dev"}
  devauth {action: "as", proxy_id: "dev", persona: "admin"}
  devauth {action: "token", proxy_id: "dev", persona: "standard", client: "story-web"}`,
	}, dt.makeDevAuthHandler())
}

func (dt *DaemonTools) makeDevAuthHandler() func(context.Context, *mcp.CallToolRequest, DevAuthInput) (*mcp.CallToolResult, DevAuthOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in DevAuthInput) (*mcp.CallToolResult, DevAuthOutput, error) {
		if in.ProxyID == "" {
			return fail[DevAuthOutput]("proxy_id is required")
		}
		if err := dt.ensureConnected(); err != nil {
			return fail[DevAuthOutput](err.Error())
		}
		status, err := dt.client.ProxyStatus(in.ProxyID)
		if err != nil {
			return fail[DevAuthOutput](fmt.Sprintf("proxy %s: %v", in.ProxyID, err))
		}
		origin, err := devAuthOrigin(getString(status, "listen_addr"))
		if err != nil {
			return fail[DevAuthOutput](fmt.Sprintf("proxy %s: %v", in.ProxyID, err))
		}
		switch in.Action {
		case "personas":
			return devAuthPersonas(origin)
		case "token":
			return devAuthToken(origin, in)
		case "as":
			return dt.devAuthAs(origin, in)
		default:
			return fail[DevAuthOutput](fmt.Sprintf("unknown action %q (use: personas, as, token)", in.Action))
		}
	}
}

// devAuthOrigin is the proxy's loopback origin, where the MCP process is a
// local caller of the issuer.
func devAuthOrigin(listenAddr string) (string, error) {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return "", fmt.Errorf("no listen address (%q)", listenAddr)
	}
	return "http://localhost:" + port, nil
}

var devAuthHTTP = &http.Client{Timeout: 5 * time.Second}

type devAuthState struct {
	Persona  string           `json:"persona"`
	Personas []DevAuthPersona `json:"personas"`
}

func fetchDevAuthState(origin string) (devAuthState, error) {
	var st devAuthState
	resp, err := devAuthHTTP.Get(origin + devoidc.Prefix + "/state")
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// 404 or the app's own page means the prefix was proxied: no issuer.
		return st, fmt.Errorf("dev-oidc not available on this proxy (HTTP %d: %s); declare a dev-oidc block in .agnt.kdl", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return st, fmt.Errorf("dev-oidc not available on this proxy (the path answered with non-issuer content); declare a dev-oidc block in .agnt.kdl")
	}
	return st, nil
}

func devAuthIssuer(origin string) string {
	resp, err := devAuthHTTP.Get(origin + devoidc.Prefix + "/.well-known/openid-configuration")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var doc struct {
		Issuer string `json:"issuer"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc)
	return doc.Issuer
}

func devAuthPersonas(origin string) (*mcp.CallToolResult, DevAuthOutput, error) {
	st, err := fetchDevAuthState(origin)
	if err != nil {
		return fail[DevAuthOutput](err.Error())
	}
	out := DevAuthOutput{Issuer: devAuthIssuer(origin), Current: st.Persona, Personas: st.Personas}
	return nil, out, nil
}

func devAuthToken(origin string, in DevAuthInput) (*mcp.CallToolResult, DevAuthOutput, error) {
	if in.Persona == "" || in.Client == "" {
		return fail[DevAuthOutput]("token needs persona and client")
	}
	req, _ := http.NewRequest(http.MethodPost, origin+devoidc.Prefix+"/mint",
		strings.NewReader(url.Values{"persona": {in.Persona}, "client": {in.Client}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	resp, err := devAuthHTTP.Do(req)
	if err != nil {
		return fail[DevAuthOutput](err.Error())
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(raw, &body)
	if resp.StatusCode != http.StatusOK {
		msg := body.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return fail[DevAuthOutput](fmt.Sprintf("mint refused (HTTP %d): %s", resp.StatusCode, msg))
	}
	return nil, DevAuthOutput{AccessToken: body.AccessToken, ExpiresIn: body.ExpiresIn,
		Message: "use as: Authorization: Bearer <access_token>"}, nil
}

// devAuthAs switches the browser: it submits the issuer's switch form in the
// page, the same request the indicator makes, so the persona cookie lands in
// the browser and the app is sent back through login.
func (dt *DaemonTools) devAuthAs(origin string, in DevAuthInput) (*mcp.CallToolResult, DevAuthOutput, error) {
	if in.Persona == "" {
		return fail[DevAuthOutput]("as needs persona")
	}
	st, err := fetchDevAuthState(origin)
	if err != nil {
		return fail[DevAuthOutput](err.Error())
	}
	known := false
	for _, p := range st.Personas {
		known = known || p.Name == in.Persona
	}
	if !known {
		names := make([]string, 0, len(st.Personas))
		for _, p := range st.Personas {
			names = append(names, p.Name)
		}
		return fail[DevAuthOutput](fmt.Sprintf("unknown persona %q; available: %s", in.Persona, strings.Join(names, ", ")))
	}
	result, err := dt.client.ProxyExec(in.ProxyID, devAuthSwitchScript(in.Persona, in.Client))
	if err != nil {
		return fail[DevAuthOutput](fmt.Sprintf("switch exec failed: %v", err))
	}
	if msg, ok := result["error"].(string); ok && msg != "" {
		return fail[DevAuthOutput]("switch exec failed: " + msg)
	}
	return nil, DevAuthOutput{Current: in.Persona,
		Message: fmt.Sprintf("switched to %s; the page is signing in again", in.Persona)}, nil
}

// devAuthSwitchScript builds the in-page form submit. Values go through
// json.Marshal so they are JS string literals, never spliced code.
func devAuthSwitchScript(persona, client string) string {
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
})()`, devoidc.Prefix+"/switch", p, c)
}
