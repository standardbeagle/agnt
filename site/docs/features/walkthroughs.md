---
sidebar_position: 6
title: Walkthroughs & Public Sharing
description: Have your AI run a live guided demo of what it just built, then publish that walkthrough as an anonymous, token-gated public page.
---

# Walkthroughs & Public Sharing

Two related features: **walkthrough mode**, where the AI demos what it just built
directly on your running app, and **publishing**, which turns that same
walkthrough into a token-gated public page you can send to someone else.

## Walkthrough mode

After the AI finishes a change, it can narrate the change *in the app* instead of
in chat. A floating step list appears over your page; each step highlights the
element it is talking about and advances on a timer, on your click, or when the
app reaches a given state.

```javascript
walkthrough {action: "start", proxy_id: "dev", script: {
  id: "demo",
  title: "New checkout",
  steps: [
    {title: "Open cart",  body: "Click the cart to begin.",
     target: "#cart-btn", gesture: "click", gesture_label: "Click to open your cart",
     advance: {type: "click-target"}},
    {title: "Totals",     body: "The order total updates live.",
     target: ".order-total", advance: {type: "auto", ms: 4000}},
    {title: "Confirm",    body: "You land on the confirm page.",
     advance: {type: "wait", when: "url-contains", value: "/confirm"}}
  ]
}}
```

**Actions:** `load` (register a script and show a replay launcher without
starting), `start`, `stop`, `next` / `prev`, `play` / `pause`, `status`, `list`.

**Advance modes:**

| `advance.type` | Behavior |
|----------------|----------|
| `auto` | Show for `ms` (default 5000), then move on |
| `click-target` | Wait for the user to click the highlighted target |
| `wait` | Wait for `url-contains`, `element-present`, or `element-visible` |

**Gestures.** A step with a `target` can render an animated affordance —
`hover`, `click`, `scroll`, or `drag` — with a `gesture_label` naming the concrete
action ("Drag the handle to reorder"). The affordance dismisses itself when the
step advances.

Scripts registered with `load` stay available behind a replay launcher in the
overlay, so you can re-run a demo later without asking the AI to rebuild it.

## Publishing a walkthrough

The `publish` tool takes a walkthrough revision and puts it behind an
unguessable share token, so a reviewer, a designer, or a customer can step
through it with no agnt install and no account.

```javascript
publish {action: "create", walkthrough: {...}}  // → id + token (SHOWN ONCE) + /s/{token}
publish {action: "list"}                        // this project's shares, no tokens
publish {action: "status", id: "<share-id>"}    // state + token hash prefix
publish {action: "rotate", id: "<share-id>"}    // new token; old one dies immediately
publish {action: "revoke", id: "<share-id>"}    // kills every route immediately
publish {action: "feedback", id: "<share-id>"}  // owner-scoped read of viewer feedback
```

### Two disjoint planes

| Plane | Who | Reached via | Can do |
|-------|-----|-------------|--------|
| **Control** | you | the `publish` MCP tool, project-scoped | create, status, list, rotate, revoke, read feedback |
| **Public** | anonymous viewer | `/s/{token}` | read the artifact, read the walkthrough/variant JSON, POST one feedback row |

The public plane is a **different HTTP handler** from the dev proxy — not the dev
proxy with auth in front of it. The `__devtool` control surface, the metrics
WebSocket, and proxy exec are never registered on it, so they are structurally
unreachable rather than merely guarded. The published artifact is self-contained:
it serves the steps from an immutable revision and loads a stripped
public-only bundle (player, variant cycler, feedback widget, boot glue).

### Secure by default

- **No public port is auto-bound.** The public listener starts only when you set
  `AGNT_PUBLIC_ADDR` (e.g. `export AGNT_PUBLIC_ADDR=":8899"`). Without it there is
  no public network surface at all.
- **Tokens are 256-bit, shown once, hashed at rest.** `create` and `rotate` return
  the plaintext token exactly once; only `sha256(token)` is stored, and only an
  8-character hash prefix appears in status output, events, and logs. A lost token
  cannot be recovered — `rotate` to mint a new one.
- **Revoke is atomic.** Every route 404s immediately, with no grace window and a
  `max-age=0, must-revalidate` cache policy so no browser serves a stale copy.
- **Unknown, revoked, and invalid tokens are indistinguishable** — every sub-route
  returns 404, so there is no existence oracle.
- **Strict CSP**, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, no
  cookies, no CORS. No `unsafe-inline`, no `unsafe-eval`.

### Anonymous feedback

Viewers can leave one feedback row per POST. Feedback is **data, never a
command**: it cannot mutate the walkthrough, the variant set, or the token, and it
reads nothing back.

| Limit | Default | KDL key |
|-------|---------|---------|
| Rate | 10 req/min per (share, IP) | `rate-per-minute` |
| Burst | 5 | `burst` |
| Body size | 4096 bytes | `max-body-bytes` |
| Retention | 500 rows per share | `max-rows-per-share` |
| Retention | 90 days | `retention-days` |

```kdl title=".agnt.kdl"
feedback {
    rate-per-minute 10
    burst 5
    max-body-bytes 4096
    max-rows-per-share 500
    retention-days 90
}
```

Read feedback through the control plane only (`publish {action: "feedback", id}`),
which enforces project ownership. Row bodies are raw and inert — escape them
before rendering as HTML. When feedback arrives, the daemon emits a counts-only,
project-scoped event carrying `share_id`, `total`, and `dropped` — never the token
and never the body.

Published shares and their feedback are stored on disk and survive daemon
restarts.

### If a token leaks

1. `publish {action: "rotate", id}` to keep the share up under a new token, or
   `publish {action: "revoke", id}` to kill it outright.
2. Watch `dropped` in the feedback response — a climbing count means the
   per-(share, IP) limiter is shedding a flood (excess is 429'd, nothing crashes).
3. Correlate log lines to shares by `token_hash_prefix`.
4. Unset `AGNT_PUBLIC_ADDR` and restart the daemon to remove the public surface
   entirely; the control plane and dev proxy stay up.
