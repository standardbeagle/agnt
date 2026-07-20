# Walkthrough: The Frame Model and Auth Breakout — iterating a checkout page

## What it is

Every top-level HTML navigation the proxy answers is wrapped: the response is a
**chrome shell** document whose body is a single content `<iframe>`, and the
real page loads inside that frame. The proxy UI (indicator, panels, sketch,
responsive mode) lives in the shell; page telemetry and the live
`window.__devtool` runtime live in the content frame. This "always-wrap" model
gives a stable interaction target and lets you resize and navigate the page
*from outside* without a reload.

The frame model has one hard conflict: identity providers refuse to render in a
frame. **Auth breakout** is the escape hatch that runs an OAuth/OIDC sign-in
*outside* the content iframe and replays the callback back into it, preserving
`sessionStorage`.

Source of truth: `docs/responsive-canonical-target.md` (frame model),
`internal/proxy/scripts/frames.js`, `internal/proxy/scripts/core.js` (exec
routing), `docs/auth-breakout.md`, `internal/proxy/authbreakout.go` +
`internal/proxy/rewrite.go` + `internal/proxy/scripts/authbreakout.js`,
`internal/config/agnt.go` (block parsing).

## Why it is unique

Resize and navigation are **reload-free**: because the page is an iframe inside
a shell you control, the shell can resize the frame or drive its history in
place, keeping page state (form fields, cart contents, open modals) intact. And
because telemetry is tagged with the emitting `frame_id`, the same error in two
frames is not collapsed — `get_errors` dedup and `proxylog`'s `LogFilter.Frames`
are frame-aware.

Auth breakout is unique because it solves the framing conflict *without*
breaking the OAuth nonce. The naive fix — navigate the whole shell to the IdP —
lands the callback in a fresh shell with fresh `sessionStorage`, and MSAL / most
OIDC clients fail their callback validation. Breakout keeps the app iframe
mounted and replays the callback into the *existing* frame, so
`handleRedirectPromise()` works with no app code change.

## Real-world scenario

You are iterating a checkout page that gates behind Microsoft Entra (Azure AD /
MSAL) sign-in. You want to:

1. Preview the checkout at mobile width without reloading and losing the cart.
2. Drive the page through the multi-step flow from the agent.
3. Actually sign in — but the moment the app navigates to
   `login.microsoftonline.com`, the content frame goes blank and the flow
   dead-ends with no error. The page just stops.

## Step by step

### 1. Start the proxy

```
proxy {action:"start", target_url:"http://localhost:3000"}
```

Expected output includes the proxy id and the `listen_addr` the shell is served
on. Open that URL — you get the chrome shell with your checkout page wrapped in
the content frame.

### 2. Resize to mobile, reload-free

```
proxy {action:"resize", width:375}
```

The shell resizes the live content frame in place — no reload, page state
preserved. `width:0` resets to full width. After resizing, the audit tools
(`api_audit`, `loading_audit`, `responsive_audit`) target the inner frame, so
they measure the page *at that viewport*.

### 3. Drive the page from outside

Navigate the content frame without leaving the shell:

```
proxy {action:"navigate", direction:"goto", target_url:"http://localhost:3000/checkout/step-2"}
```

`direction` accepts `back`, `forward`, `reload`, or `goto` (with `target_url`).
The navigation is deferred a microtask so the exec reply returns before unload.

### 4. exec against inner vs outer

By default exec scripts the **content frame** (the page):

```
proxy {action:"exec", code:"return document.querySelector('#cart-total').textContent"}
```

To script the **chrome shell** (proxy UI / host) instead, target the outer
frame via the `@chrome` role token:

```
proxy {action:"exec", target:"outer", code:"return window.__devtool_frame_role"}
```

`target:"inner"` is the default. On a multi-frame page, address one specific
content frame by id:

```
proxy {action:"exec", frame_id:"<fid>", code:"..."}
```

Untargeted exec and `frame_id`-targeted exec are handled in `core.js`
`handleServerMessage`: a frame runs the code only when the message is untargeted,
addressed to its own `frame_id`, or addressed to `@chrome` and it is the chrome
frame — so a multi-frame page never produces duplicate replies.

### 5. Sign-in dead-ends — enable auth breakout

The navigation to Entra blanks the frame. Add the block to `.agnt.kdl`.
Declaring the block is the opt-in; the default pattern set covers the common
providers, so the minimal form is just:

```kdl
auth-breakout
```

Explicitly, with defaults spelled out (from `docs/auth-breakout.md`):

```kdl
auth-breakout {
    enabled true
    mode "popup"
    patterns "login.microsoftonline.com" "figma.com/oauth"
}
```

Scope is project-wide: every proxy the daemon creates for the project picks the
rules up, on every creation path (autostart, URL detection, fallback port,
explicit `proxy start`, restart, restore). The config is reconciled live, so an
existing proxy adopts a newly added block.

### 6. What happens on sign-in now

The auth navigation is intercepted two ways:

- **Server-side 3xx**: a backend redirect straight to the IdP is replaced by a
  200 stub page (`interceptAuthRedirect` in `rewrite.go`) whose script calls
  `parent.__devtool_auth.breakout(url)`.
- **Client-side**: `authbreakout.js` watches the content frame — the Navigation
  API `navigate` event (Chromium) and a capture-phase anchor-click listener (all
  browsers).

In `popup` mode (the default) the IdP opens in a named popup while the app
iframe stays mounted. The user signs in, the IdP 302s to the callback, and the
popup relays the callback URL back to the opener, which does
`location.replace(callback)` into the *existing* iframe. Because the callback is
replayed into the same iframe, `sessionStorage` is untouched and MSAL's nonce
survives.

### 7. MSAL-specific one-liner

`msal-browser` throws `redirect_in_iframe` *before it navigates*, so breakout
never sees the request. In your dev config only:

```js
new PublicClientApplication({
  auth: { /* … */ },
  system: { allowRedirectInIframe: true },
})
```

The library then performs the navigation, breakout carries it out of the frame,
and `handleRedirectPromise()` sees the callback with its nonce intact.
`loginPopup` flows work without this flag and without breakout at all.

## Gotchas

- **`allowRedirectInIframe` is dev-only.** Never enable it in a production MSAL
  config — framing protections exist for a reason. The breakout is a development
  tool and is off unless the project opts in.
- **How the popup identifies itself: two signals, both cleared before relay.**
  `window.name` (Chrome preserves it; Safari/ITP drops it on cross-origin nav)
  and a `sessionStorage` marker (`window.open` clones the opener's
  `sessionStorage`). Either one identifies the popup; both are cleared before the
  relay fires so a throwing opener cannot leave a live marker that re-fires. This
  is why the fix identifies by `sessionStorage`, not `window.name` alone.
- **Firefox/Safari `location.href = <idp>` cannot be intercepted from JS.** The
  server-side 3xx path and anchor-click capture cover every other route. If your
  app assigns `location.href` directly and you need those browsers, call
  `window.__devtool_auth_breakout(url)` manually.
- **Patterns match *outbound* navigation targets, not incoming requests.** A
  pattern cannot make the proxy serve or trust a foreign origin. `*` matches any
  run of characters; everything else is a case-insensitive substring. Only
  navigations leaving the proxy origin are candidates.
- **The Go matcher and the JS matcher are hand-mirrored.** `AuthBreakout.MatchesURL`
  (Go) and `matches()` (`authbreakout.js`) implement the same semantics against
  the same pattern list; keep them in sync when editing.
- **`top` mode reloads the app.** If a provider refuses to run in a popup, use
  `mode "top"` — the whole shell navigates to the IdP and the return redirect is
  wrapped again, but you lose in-page state. If the browser blocks `window.open`,
  `popup` falls back to `top` automatically rather than failing.
- **Only genuine top-level navigations are wrapped.** Specifically, wrapping
  requires `Sec-Fetch-Dest: document`. Requests from `fetch()`
  (`Sec-Fetch-Dest: empty`), nested browsing contexts, headerless clients, and
  requests carrying the `__devtool_frame` marker are served unwrapped.
