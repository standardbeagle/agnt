# Auth Breakout

Runs OAuth / OIDC sign-in flows outside the proxy's content iframe, then
replays the callback back into it.

Config reference: [configuration.md](configuration.md#auth-breakout).
Code: `internal/config/agnt.go` (parse), `internal/proxy/authbreakout.go` +
`internal/proxy/rewrite.go` (server), `internal/proxy/scripts/authbreakout.js`
(browser), `internal/daemon/hub_helpers.go` (wiring).

## The problem

The proxy uses an always-wrap frame model: a top-level navigation is answered
with a *chrome shell* document that hosts your app in a content `<iframe>`.
That is what makes the overlay, sketch mode, and viewport resizing work.

Identity providers refuse to render in a frame. `login.microsoftonline.com`,
Google, GitHub, Okta, Auth0 and Figma all send `X-Frame-Options: DENY` or a
`frame-ancestors` CSP. So the moment your app navigates to the IdP, the
content frame goes blank and the flow dead-ends. Nothing errors — the page
just stops.

Worse, the frame is the wrong place to fix it. Navigating the *shell* to the
IdP would work, but the callback then lands in a fresh shell with a fresh
`sessionStorage`, and libraries that stash a request nonce there before
redirecting (MSAL, most OIDC clients) fail their callback validation.

## The model

The breakout has one job: get the auth navigation out of the iframe, and get
the callback URL back *in* — same tab, same `sessionStorage`.

```
content iframe          chrome shell              popup
─────────────────       ──────────────────        ──────────────
nav to IdP ────────────▶ breakout(url) ─────────▶ open(idp)
  (intercepted)                                     │
                                                    │ user signs in
                                                    ▼
                                                  IdP 302 → /callback
                                                    │
                        complete(callbackUrl) ◀─────┘ (relay, then close)
   ◀────── location.replace(callback) ─┘
```

Because the callback is replayed into the *existing* iframe, the app's
`sessionStorage` is untouched. `handleRedirectPromise()` and equivalents work
with no code change.

## Enabling it

Declaring the block is the opt-in. The default pattern set covers the common
providers, so most projects need no keys at all:

```kdl
auth-breakout
```

Explicitly, with the defaults spelled out:

```kdl
auth-breakout {
    enabled true
    mode "popup"
    patterns "login.microsoftonline.com" "figma.com/oauth"
}
```

Scope is project-wide: every proxy the daemon creates for the project picks
the rules up, on every creation path (autostart, URL detection, fallback port,
explicit `proxy start`, restart, restore).

## Modes

**`popup`** (default) — auth runs in a named popup; the app iframe stays
mounted the whole time. This is the mode that preserves in-page state, and the
one to use unless a provider actively breaks under it.

If the browser blocks `window.open`, breakout falls back to `top` rather than
failing. You lose in-page state, but the flow completes.

**`top`** — the whole shell navigates to the IdP. The return redirect
re-enters the proxy and is wrapped again. Use when a provider refuses to run
in a popup, and accept that the app reloads.

## What gets intercepted

An auth navigation can leave the app by two different routes, so both are
covered.

**Server-side 3xx.** Your backend answers a content-frame request with a
redirect straight to the IdP. `interceptAuthRedirect` (`rewrite.go`) replaces
that response with a 200 stub page whose script calls
`parent.__devtool_auth.breakout(url)`. This runs before `Location` rewriting —
a breakout target is external by definition and never the proxied backend.

Only content-frame requests are intercepted. A genuine top-level navigation to
the IdP is not framed and needs no breakout.

**Client-side navigation.** `authbreakout.js` watches the content frame:

| Route | Mechanism | Coverage |
|-------|-----------|----------|
| `location.href = <idp>`, `location.assign`, meta refresh, form GET | Navigation API `navigate` event | Chromium only |
| `<a href="<idp>">` click | capture-phase click listener | all browsers |
| anything else | `window.__devtool_auth_breakout(url)` | manual escape hatch |

The gap is real and worth stating plainly: on Firefox and Safari, a
client-side `location.href = <idp>` assignment cannot be intercepted from JS.
The server-side 3xx path and the anchor capture cover every other route. If
your app assigns `location.href` directly and you need those browsers, call
`window.__devtool_auth_breakout(url)` instead.

## Pattern matching

Patterns are case-insensitive fragments matched against the full navigation
URL. `*` matches any run of characters; everything else matches as a
substring. `login.microsoftonline.com` matches any path on that host;
`figma.com/oauth` matches only the OAuth paths.

Only navigations *leaving the proxy origin* are candidates — a same-origin URL
goes through the proxy and never needs a breakout.

The Go matcher (`AuthBreakout.MatchesURL`) and the JS matcher (`matches()` in
`authbreakout.js`) implement the same semantics against the same pattern list.
They are mirrored by hand; keep them in sync.

## MSAL

`msal-browser` throws `redirect_in_iframe` *before it navigates*, so the
breakout never sees the request and nothing happens. Set, in your dev config
only:

```js
new PublicClientApplication({
  auth: { /* … */ },
  system: { allowRedirectInIframe: true },
})
```

The library then performs the navigation, breakout carries it out of the
frame, and `handleRedirectPromise()` sees the callback in the original tab
with its nonce intact. Popup-based MSAL flows (`loginPopup`) work without
this flag, and without the breakout at all.

## How the popup identifies itself

When the popup returns to the app origin, it must recognise that it is an auth
popup and relay its URL to the opener rather than rendering the app. Two
independent signals mark it, because neither is durable everywhere:

- **`window.name`** — set by `window.open`. Chrome preserves it across the
  proxy → IdP → proxy round trip. Engines with anti-tracking name clearing
  (Safari/ITP) drop it on the cross-origin navigation.
- **`sessionStorage`** — `window.open` clones the opener's `sessionStorage`
  for the opener's origin into the new browsing context, so the marker is
  present when the popup navigates back to that origin. Absent when storage is
  blocked.

Either identifies the popup. Both are cleared before the relay fires, so a
throwing opener cannot leave a live marker that re-fires on the window's next
same-origin load. The opener is distinguished from its own popup by the
`window.opener` check — a shell has no opener.

`TestE2E_AuthBreakout_PopupMarkerSurvivesNameLoss` opens the popup under a
foreign window name so only the `sessionStorage` marker can identify it; it
fails if either half of the marker is removed.

## Security notes

The breakout is a **development** tool and is off unless the project opts in.

- Patterns match *outbound navigation targets*, not incoming requests. A
  pattern cannot be used to make the proxy serve or trust a foreign origin.
- The callback URL is replayed into the iframe with the frame marker attached
  as a **query** parameter, never in the hash — OAuth implicit-flow tokens
  commonly live in the fragment and must reach the app byte-for-byte.
- The stub page served in place of a 3xx carries `Cache-Control: no-store` and
  interpolates the target URL as a JSON-escaped JS string literal.
- Do not enable `allowRedirectInIframe` in a production MSAL config. It exists
  to let the dev proxy see the navigation; framing protections are there for a
  reason.

## Tests

| Test | Covers |
|------|--------|
| `TestE2E_AuthBreakout_PopupRoundTrip` | click → intercept → popup → relay → callback in the *same* iframe; shell realm survives |
| `TestE2E_AuthBreakout_PopupMarkerSurvivesNameLoss` | `sessionStorage` marker alone identifies the popup |
| `TestE2E_AuthBreakout_ServerRedirectStub` | backend 3xx → stub → breakout → callback |
| `internal/proxy/authbreakout_test.go` | pattern matching, stub construction, client config JS |
| `internal/config/agnt_test.go` | block parsing, tri-state enable, mode validation |

The e2e tests need a real Chrome (`skipIfNoBrowser`). Their fake IdP is served
on `localhost` while the proxy stays on `127.0.0.1`: two ports on one IP are
cross-origin but same-*site*, and a same-site IdP would not exercise the
navigation class that clears `window.name`.
