---
sidebar_position: 8
title: Dev Sign-in & Protected Sharing
description: Sign the app you are building in as a standard user or an admin with agnt's dev OIDC issuer, over loopback or your tailnet, and put a dev proxy on a stable hostname behind Cloudflare Access.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Dev Sign-in & Protected Sharing

Testing an app with real sign-in usually means juggling real accounts in a real
identity provider: one for a standard user, another for an admin, a third for
the tenant that broke. agnt removes that setup. It serves a **dev-only OIDC
issuer** on your dev proxy, with test users (*personas*) declared in
`.agnt.kdl`, and lets you, or your agent, switch between them in one click.

When the app has to be reachable from somewhere else, a **named Cloudflare
tunnel** puts the proxy on a stable hostname you own, with Cloudflare Access in
front and the Access token checked again at the origin.

<DemoVideo
  src="/video/dev-oidc-personas.webm"
  poster="/img/dev-oidc-personas-poster.webp"
  captions="/video/dev-oidc-personas.vtt"
  label="Demo: the story app on a build box, browsed over the tailnet. It signs in through agnt's dev issuer, which shows a persona picker; the dev picks Dev Standard, then switches to Dev Admin from the persona chip in the agnt indicator, and the app signs in again as admin." />

## Dev OIDC personas

Add a `dev-oidc` block to `.agnt.kdl`. Every proxy of the project then answers
OIDC at `/__agnt/oidc/` (discovery, `authorize`, `token`, `userinfo`, `jwks`,
`logout`).

```kdl
dev-oidc {
    clients {
        my-app {
            redirect-uri "http://localhost:*/api/auth/callback/dev-oidc"
            secret "dev-only"
            login-path "/auth/signin"
            session-cookies "authjs.session-token"
        }
    }
    personas {
        standard {
            email "std@example.com"
            name "Dev Standard"
            roles "user"
        }
        admin {
            email "admin@example.com"
            name "Dev Admin"
            roles "admin" "user"
            claims {
                tenant "acme"
            }
        }
    }
    default-persona "standard"
}
```

Point the app's OIDC provider at the issuer
(`http://localhost:<proxy port>/__agnt/oidc`) with the client id, secret and
redirect URI you declared. Tokens are RS256 and carry `email`, `name`,
`preferred_username`, `roles` and any extra `claims`, with
`sub = agnt-dev|<persona>`. The signing key lives in memory for the daemon's
lifetime, so it is never written to disk.

`secret` is a dev-only literal. It protects nothing outside this issuer, so it
can live in the checked-in file.

### Switching personas

There are three ways to switch, and all three send the same switch request. The
issuer sets the persona, expires the app's session cookies (`session-cookies`)
and sends the app back to its `login-path`, so it signs in again as the new
user.

| Where | How |
|---|---|
| Browser | The **persona chip** in the agnt indicator panel (`as: Dev Standard`). Pick another persona from its menu. |
| Terminal | `:as admin` in the `agnt run` overlay palette. |
| Agent | The `devauth` MCP tool: `{action: "as", proxy_id: "dev", persona: "admin"}`. |

`devauth` also lists personas (`action: "personas"`) and mints an access token
for API calls (`action: "token"`), so an agent can check an endpoint as each
role without driving the login page.

### Who gets a persona

The issuer lets whoever reaches it become someone, so agnt decides by the
listener a request arrived on:

- **Loopback** (the proxy's own `localhost` port, no tunnel, no public URL):
  every persona. `default-persona` signs automated and agent logins in without
  stopping at the picker.
- **Tailnet** (`bind "tailscale"`): agnt runs `tailscale whois` on the
  connection's peer and offers only the personas that `allow` grants to that
  login. The picker always shows, so you choose who you are.
- **A `cloudflare-tunnel` with Access**: only the personas `allow` grants to
  the verified Access email.
- **Anything else**, including an unauthenticated tunnel: `403` on every
  route.

```kdl
dev-oidc {
    // ...clients and personas as above...
    allow {
        "you@example.com" "standard" "admin"
    }
}
```

## Over the tailnet

The demo above is this setup: the app runs on a build box you work on over
SSH, and you browse it from your laptop. Bind the proxy to the tailnet, and
leave `issuer` at its default (or set it to the MagicDNS URL). The browser and
the app's backend then use the same issuer URL.

```kdl
proxies {
    dev {
        script "dev"
        bind "tailscale"
        listen-port 31536
    }
}
```

The app's back-channel calls (discovery, token) come from the node's own
tailnet address, and `tailscale whois` maps that address to the node's owner.
That owner is usually you, so one `allow` entry covers both the browser and the
backend. `:tailscale` in the overlay warns when the block still pins a loopback
issuer or has an empty `allow`.

## Named Cloudflare tunnel with Access

A quick tunnel gives you a random `*.trycloudflare.com` name, which is no good
for OAuth redirect URIs or Access policies. A **named tunnel** runs in front of
one proxy at a hostname you own:

```kdl
proxies {
    dev {
        url "http://localhost:5173"
        cloudflare-tunnel {
            id "6ff42ae2-765d-4adf-8112-31c55c1551ef"
            hostname "dev.example.com"
            credentials-file "~/.config/cloudflared/dev.json"
            access {
                team-domain "yourteam.cloudflareaccess.com"
                aud "<Access application Audience tag>"
            }
        }
    }
}
```

<DemoVideo
  src="/video/cloudflare-access.webm"
  poster="/img/cloudflare-access-poster.webp"
  captions="/video/cloudflare-access.vtt"
  label="Demo: a proxy declared with a cloudflare-tunnel block at agnt-demo.sbdev.io. curl without login gets a 302 to the Cloudflare Access login; a forged Access header sent straight to agnt's ingress gets 403; an Access service token gets through. In the browser, the Access login page, then the app showing it was reached over https at the public hostname." />

- **Fail closed.** The block needs either `access { ... }` or an explicit
  `allow-unauthenticated true`. Without one of them, the config doesn't parse.
- **Checked at the origin too.** cloudflared talks to a dedicated loopback
  listener that verifies `Cf-Access-Jwt-Assertion` on every request, WebSocket
  upgrades included: RS256, a key from the team's JWKS, the right issuer and
  audience, and a token that hasn't expired. A misconfigured Access
  application gets a `403`, not your dev server.
- **HTTPS-aware.** Requests through the tunnel reach the app with
  `X-Forwarded-Proto: https` and `X-Forwarded-Host: <hostname>`, so OAuth
  `redirect_uri`s built from forwarded headers come out right.
- **Follows the proxy.** The tunnel starts and stops with the proxy and
  survives restarts and config reconciles. A tunnel failure surfaces as a
  `named_tunnel_failed` diagnostic.

agnt never holds your Cloudflare API token. Create the tunnel, its DNS route
and the Access application once from a workstation (`cloudflared tunnel
create`, `cloudflared tunnel route dns`), then point the block at them.

Combine the tunnel with `dev-oidc` and the whole team can sign in to your dev
build as the personas `allow` gives their Access email.

## Reference

- [`.agnt.kdl` reference](../agnt-kdl.md)
- Full configuration detail: `docs/configuration.md` § *Dev OIDC* and § *Named
  Cloudflare Tunnel* in the repository
- `devauth` tool: `docs/mcp-tools.md` § *devauth*
