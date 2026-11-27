# Dev OIDC issuer: personas you can switch from the proxy UI

Status: built (2026-09-27); this document records the shipped design. Builds on the named Cloudflare
tunnel + Access work (`cloudflare-tunnel`, commits `ffcbf77d`..`fd0a4df3`).

## 1. Goal

A developer testing an app that logs in with OIDC wants to be *standard user*
one minute and *admin* the next, without a real IdP account per role and
without leaving the browser. An agent wants the same thing programmatically.

agnt serves a small OIDC issuer on the proxy's own origin. Personas (users and
their claims) live in `.agnt.kdl`. The app's dev config points its OIDC
authority at the proxy; nothing else in the app changes. The browser indicator
shows the current persona and switches it; `:as <persona>` in the overlay
palette and the `devauth` MCP tool do the same.

Through a named tunnel, the person first logs in with Cloudflare Access
(GitHub/Google, registered once per Cloudflare account). The verified Access
email then decides which personas that person may assume. Access is the
"tunnelled dex" the owner asked for; no second IdP service is run.

### Non-goals

- A real identity provider. No passwords, no user store, no consent screen, no
  federation of its own (Access is the federation).
- Production use. Keys are ephemeral, tokens say `agnt-dev` in their issuer
  metadata, and the issuer refuses every exposure it cannot authenticate (§5).
- Refresh-token rotation, dynamic client registration, implicit/hybrid flows,
  device flow, SAML.

## 2. Config

Project-level block, applied to every proxy of the project (same chokepoint as
`auth-breakout`: `Daemon.wireProxyLogger` → `applyDevOIDC`).

As shipped (see `docs/configuration.md` § Dev OIDC for the reference):

```kdl
dev-oidc {
    issuer "https://dev.sbdev.io/__agnt/oidc"   // optional
    clients {
        story-web {
            redirect-uri "http://localhost:*/auth/callback" "https://dev.sbdev.io/auth/callback"
            secret "dev-only"
            audience "story-api"
            login-path "/auth/login"
            session-cookies "story.sid"
        }
    }
    personas {
        standard {
            email "andy+std@standardbeagle.com"
            roles "user"
        }
        admin {
            email "andy+admin@standardbeagle.com"
            roles "admin" "user"
            claims {
                tenant "acme"
            }
        }
    }
    default-persona "standard"
    allow {
        "andybrummer@standardbeagle.com" "standard" "admin"
    }
}
```

Parse-time validation (fails loud, `ParseAgntConfig`):

- at least one `client` and one `persona`; names unique; `default-persona`
  names a declared persona;
- `redirect-uri`: absolute `http`/`https` URL, no fragment; `*` allowed only as
  the whole port (`localhost:*`, loopback hosts only). No other wildcards.
- `persona` names match `^[a-z0-9][a-z0-9_-]{0,31}$`; `email` required;
  reserved claim names (`iss sub aud exp iat nbf nonce azp auth_time`) refused
  inside `claims`.
- `allow` entries name declared personas.
- `secret`, when present, is a dev-only literal: it protects nothing outside
  this issuer (the issuer only mints tokens for these personas), so it is
  allowed in the checked-in file. The docs say so.

## 3. Endpoints (on every proxy of the project, mux prefix `/__agnt/oidc/`)

| Path | Method | Purpose |
|---|---|---|
| `/.well-known/openid-configuration` | GET | Discovery. `issuer` is always the configured issuer. Browser-facing endpoints (`authorization_endpoint`, `end_session_endpoint`) are built from the issuer. Back-channel endpoints (`token_endpoint`, `jwks_uri`, `userinfo_endpoint`) are built from the issuer too, except when the discovery request itself is local (§5), where they use the proxy's local origin. See §3.1. |
| `/jwks` | GET | Public signing key(s). |
| `/authorize` | GET | Authorization code + PKCE. Resolves the persona (§4) and redirects with `code`, or renders the picker. |
| `/pick` | POST | Picker form submit: sets the persona cookie, resumes the pending authorize. |
| `/token` | POST | `authorization_code` (PKCE S256 required for public clients; client secret for confidential ones) and `refresh_token`. |
| `/userinfo` | GET | Claims for a bearer access token. |
| `/logout` | GET | `end_session_endpoint`: clears the persona's app session state, redirects to `post_logout_redirect_uri` if registered. |
| `/switch` | POST | Persona switch (§4.3). |
| `/state` | GET | JSON `{persona, personas[]}` for the indicator, the palette and the MCP tool. |
| `/mint` | POST | Local callers only, same-origin: an access token for `persona` + `client`, backing `devauth {action:"token"}`. |

### 3.1 Back channel through a tunnel

With `issuer "https://dev.sbdev.io/..."`, the browser reaches authorize through
the tunnel and Access. The app's *backend*, though, calls discovery, JWKS and
token server-side; through Cloudflare those calls carry no Access login and are
refused at the edge. So the backend fetches discovery from the proxy's local
URL (`http://localhost:<port>/__agnt/oidc/.well-known/openid-configuration`,
e.g. .NET `MetadataAddress`, openid-client `Issuer.discover(localUrl)`), and a
local discovery request gets local back-channel endpoints. `iss` in every token
stays the configured issuer, so browser and backend agree. Libraries that insist
the discovery URL equals the issuer are the open follow-up in §8.

Tokens:

- RS256, one key per project per daemon lifetime, held in memory only. A daemon
  restart means a new key and a re-login, which is the honest dev behaviour and
  keeps no private key at rest.
- `id_token`: `iss sub aud exp iat nonce azp email email_verified name
  preferred_username roles` + the persona's `claims`. `sub` = `agnt-dev|<persona>`.
- `access_token`: JWT with the same identity claims, `aud` = client `audience`,
  `scope`, `client_id`. Lifetime 1h; `refresh_token` opaque, 24h, in-memory.
- Codes: single use, 60s, bound to client, redirect URI, PKCE challenge and
  persona.

## 4. Persona resolution and switching

### 4.1 Who may be which persona

`callerPersonas(r)` returns the set of personas the request may assume:

| Arrived on | Allowed personas |
|---|---|
| Proxy's own listener, and the local-origin checks in §5 pass | all |
| Named-tunnel ingress, Access-verified | those listed by `allow` for the verified Access `email` claim (none if unlisted) |
| Anything else | none; every `/__agnt/oidc/` route answers 403 |

The Access guard hands the verified claims to the handler through the request
context, so the email comes from the verified token only, never from a header.

### 4.2 Choosing at authorize time

1. If the `__agnt_persona` cookie names an allowed persona and `prompt` is not
   `login`/`select_account`: issue the code silently.
2. Else if exactly one persona is allowed, or `default-persona` is allowed and
   the request came from the local listener: use it silently. (Automation and
   agent logins never see a page.)
3. Else render the picker: one button per allowed persona, showing name, email
   and roles. Submitting POSTs `/pick`.

The cookie is a preference, not an authorization: every use re-checks it
against `callerPersonas`. `HttpOnly; SameSite=Lax; Path=/__agnt/oidc`,
`Secure` on the tunnel.

### 4.3 Switching

`POST /switch` with `persona=<name>`:

1. same-origin check (Origin must be the proxy origin or its public URL, as for
   the control WebSocket) and `callerPersonas` membership;
2. set `__agnt_persona`;
3. expire every cookie named in the clients' `session-cookies` (Set-Cookie,
   `Max-Age=0`, `Path=/`). Refresh tokens are not revoked: the issuer cannot
   tell which browser holds one, and expiring the app's session is what forces
   the new login;
4. `303` to the client's `login-path` (or `/` when none is declared). The app
   starts a login, authorize takes step 1, and the app is now signed in as the
   new persona.

All three UI surfaces use this one endpoint:

- **Browser indicator**: a persona chip in the indicator header (chrome role)
  showing the current persona; clicking opens a list of allowed personas; a
  choice submits `/switch` in the content frame.
- **Overlay palette** `:as <persona>` and **MCP** `devauth {action:"as"}`: the
  daemon runs a `PROXY EXEC` in the active page that submits the same form.
- **MCP** `devauth {action:"token", persona, client}`: returns an access token
  minted directly (no browser), for API tests. Local-only by construction: MCP
  is a local surface.
- **MCP** `devauth {action:"personas"}`: lists personas and the current one.

## 5. Exposure rules (fail closed)

The issuer is a login bypass by design, so where it answers is the security
boundary. On the proxy's own listener a request is **local** only if all hold:

1. the proxy is bound to loopback (not `0.0.0.0`, a LAN address, or
   `bind "tailscale"`);
2. the `Host` is a loopback authority (`localhost`, `127.0.0.1`, `[::1]`);
3. no quick tunnel is bound to the proxy (no live tunnel public-URL binding);
4. no reverse-proxy fingerprint is present: `Cf-Ray`, `Cf-Connecting-IP`,
   `Tailscale-User-Login`, `X-Forwarded-For`, `Forwarded`, any `ngrok-*`.

(4) catches a tunnel agnt did not start (a hand-run `cloudflared --url`). Using
client-controlled headers is safe here because they can only *remove* access.

Otherwise the request is refused unless it arrived on a named-tunnel ingress
with a verified Access token (§4.1). A tunnel with `allow-unauthenticated true`
never serves the issuer.

## 6. Components

| Piece | Where |
|---|---|
| Config structs + validation | `internal/config/dev_oidc.go` (redirect-pattern rule in `internal/devoidc/redirect.go`) |
| Issuer (keys, codes, tokens, persona policy, HTTP handler) | new `internal/devoidc/` package, no dependency on `proxy` |
| Access claims in request context | `internal/proxy/cfaccess.go` (`Guard` stores verified claims) |
| Local-origin classification + mount on the proxy mux and tunnel ingress | `internal/proxy/devoidc.go` |
| Project wiring (one issuer per project, shared by its proxies) | `internal/daemon/devoidc.go`, called from `wireProxyLogger` |
| `devauth` MCP tool (no hub verb: the MCP process is a local caller of the proxy's endpoints) | `internal/tools/devauth_tool.go`, shared client in `internal/devoidc/client.go` |
| Overlay palette `:as` | `internal/overlay/proxy_commands.go` (`runAsCommand`) |
| Indicator persona chip | `internal/proxy/scripts/persona-chip.js` (chrome role), mounted by `indicator.js` |
| Docs | `docs/configuration.md` § Dev OIDC, `docs/mcp-tools.md` |

## 7. Tests

- `devoidc`: full code+PKCE flow against a real `http.Handler`; token claims
  per persona; refresh; code reuse, wrong verifier, wrong redirect URI, wrong
  client secret, expired code, persona outside the allowed set → each refused
  with its own error; `iss` fixed regardless of Host. Validation of tokens with
  a stock JOSE check (signature against `/jwks`).
- Proxy: each §5 condition independently flips a local request to 403
  (mutation-verify one); Access-verified tunnel request gets exactly the
  `allow`-listed personas; unlisted email gets none; switch expires the
  declared cookies and redirects to `login-path`; cross-origin switch refused.
- Config: parse + every validation refusal.
- JS tier (vitest/jsdom): the indicator chip renders the `/state` personas and
  submits `/switch`.
- End to end: a tiny relying-party test server using the standard flow logs in
  as `standard`, switches to `admin`, and sees the new `roles` claim.

## 8. Open follow-ups (not in this build)

- Back channel for libraries that require discovery URL == issuer: an Access
  bypass for the back-channel paths plus a matching exemption on the tunnel
  ingress guard.

- Map an Access identity straight to a default persona (`allow ... default`).
- Persist the signing key per project so a daemon restart keeps sessions.
- Service-token (`common_name`) callers for machine-to-machine tests over the
  tunnel.

## 9. Addendum (2026-09-28): tailnet callers

Working on a remote box over SSH means browsing its proxy over the tailnet.
A tailnet-bound proxy now identifies callers the way the Access tunnel does:
the connection's peer address must be a tailnet address, `Host` must be one of
the node's own tailnet names, no quick tunnel / static public-url / relay
header, and `tailscale whois <peer>` must name a person (tagged devices are
refused). That login is looked up in the same `allow` list. `mint` is open to
local and tailnet callers, limited to their allowed personas. The default
issuer follows the bind (`http://<MagicDNS name>:<port>/__agnt/oidc`), and the
devauth tool and `:as` reach the proxy on its real listen address.
`:tailscale` warns when the dev-oidc block still pins a loopback issuer or has
an empty `allow`.
