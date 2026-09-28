---
paths:
  - "internal/publish/**"
  - "internal/proxy/public_*"
  - "internal/proxy/rewrite*"
  - "internal/proxy/server.go"
  - "internal/proxy/mediatype.go"
  - "internal/proxy/named_tunnel.go"
  - "internal/proxy/cfaccess.go"
  - "internal/proxy/injector.go"
  - "internal/daemon/publish*"
  - "cmd/agnt/publish*"
  - "internal/proxy/scripts/variant-engine.js"
  - "internal/proxy/scripts/feedback-client.js"
  - "internal/proxy/scripts/demo-indicator.js"
---

# Publish / public-plane security rules

Review procedure for any boundary change (adversarial review, mutation checks, doc-claim checks, planner checks) is in the `security-boundary-review` skill. The sections below are code rules. Section numbers are cited from code; keep them.

## 1. Green suites do not prove a boundary
Author tests prove the property the author intended, not the one that matters. Run the `security-boundary-review` skill.

## 2. Content digests are never a read or access key
A digest works as a dedup or version key. Scope every read by owner or share identity, and carry the digest only as metadata on records that are already owner-scoped. Why: keying by digest merges records across tenants whose content collides. Regression test: `TestFeedbackReadCollidingDigestNoCrossProjectLeak`.

## 3. Close input classes at the validator that owns them
CSP neutralizing an input does not mean the boundary closed it. Validators must reject equivalent spellings, not scan for literals. Examples: the endpoint guard in `feedback-client.js` must reject a protocol-relative `//host`; `forbiddenPublicTokens` is a literal scan that concatenated strings evade.

## 4. go-cli-server fork is a recurring defect source
Send process-lifecycle fixes upstream rather than re-patching the vendor copy. See `platform-build-and-flake-lessons.md`.

## 5. Every validated input needs a consumer
A config key, op field, CLI flag or wire field that validates but is never read gets accepted and then silently dropped. Until a consumer exists, reject it at validation with a "not implemented" reason. Open instance: the `addScript src` op (`internal/publish/op.go`) has no consumer, tracked in `01KYQJ9ARWQ0YMGWZ199B6SA3F`. Adding a validator does not retire the raw casts that predate it, so sweep existing call sites; for example, `hub_tunnel.go` casts straight to `tunnel.Provider`.

## 6. Planner check: the slice set covers the DoD
Procedure is in the skill.

## 7. SSRF guard tests: the seam observes the control, it does not remove it
The deny-list refuses every address a test can bind. `guardedUpstreamFetcher` (`public_routes.go`) exposes `resolve`, `dial` and `tlsConfig` seams whose zero value is the production path. The test resolver returns a public IP. The test dialer asserts it received exactly that IP, then redirects to the local listener. The hostname stays `example.com` so TLS verification still runs. Why: a guard that validates the hostname and then lets `http.Transport` re-resolve it is defeated by DNS rebinding.

## 8. Assert that the control refused, not just that the call errored
Procedure is in the skill.

## 9. A criterion's owning file must be in the fileScope
Procedure is in the skill.

## 10. Doc claims about security controls count as code
Procedure is in the skill. `CheckUpstreamOrigin` has one production caller, the upstream document fetch plus its redirect walk.

## 11. Briefs and fix_hints are hypotheses
Procedure is in the skill.

## 12. Make the dangerous input unrepresentable
`Store.ReconcileFiles` revokes shares irreversibly, which INV-4 requires. A failed enumeration must never look like an empty one, so `loadWalkthroughDir` returns either a complete set or an error, and `dirFingerprint` never turns a read error into an empty result. Reconcile runs only after every file has published, and there is exactly one call site (`cmd/agnt/publish_serve.go`). For any irreversible diff-driven batch operation (delete, revoke, GC), fix the loader's return type rather than the caller. Open residual: a directory that is present but unmounted reads as empty, which needs a two-poll confirmation.

## 13. Planner check: integration points exist on the claimed path
Procedure is in the skill. Before scoping a public-plane outcome, check `rolePublicModules`, a closed allowlist. A `RoleChrome` module or `BuildShellDocument` (dev-only) cannot affect a public response.

## 14. Style injected UI through CSSOM, never by widening CSP
The public plane authorizes no inline style. Use a constructed `CSSStyleSheet` via `adoptedStyleSheets` on a closed shadow root, with `:host{all:initial!important}` (see `demo-indicator.js`). The script-src hash and SRI are derived from content (`cspHash` in `injector.go`), so adding bytes re-pins them automatically. A header edit for a new bundle member is a red flag. Two robustness cases need separate owners: hostile removal and incidental removal (for example, SPA `innerHTML` hydration).

## 15. Rewrite only the Origin the proxy manufactured
The Director in `server.go` rewrites `Origin` only when `url.Parse(Origin).Host` equals the pre-Director `req.Host`. It forwards third-party and absent Origins verbatim. Why: a blanket rewrite launders cross-site requests into same-origin ones and defeats the backend's CSRF checks. The same rule applies to `Referer` and `Host`. `checkWSOrigin` runs on `/__devtool_metrics`, which never goes through the Director. Test: `origin_rewrite_test.go` asserts the exact forwarded value.

## 16. MAC the exact bytes you will fetch
Subresource references (`internal/publish/subresource.go`, `public_subresource.go`) carry an HMAC over the length-prefixed tuple `(shareID, url, depth)`. `Verify` and the fetch consume the same `q.Get("u")` string, with no re-normalization in between. Every bound field goes in the tuple. The key is per-daemon, in memory and generated by a CSPRNG, so a restart fails closed. With a nil signer the route refuses and nothing is rewritten.

## 17. On anonymous routes, refuse what you would decode
`readUpstreamDocument` and `readUpstreamSubresource` refuse any non-identity `Content-Encoding` with a loud 502. The dev plane decodes the same encodings. Why: an unsolicited encoding opens a decompression-bomb surface that an anonymous viewer can aim at a third party. Keep the divergence and keep the comment that explains it. Match media types with the exact predicates in `mediatype.go` (`mime.ParseMediaType`, deny on parse failure), never with `strings.Contains`.

## 18. Egress routes share one guard walk
`fetchDocument` and `fetchSubresource` both call `guardedUpstreamFetcher.walk()`, which re-checks `CheckUpstreamOrigin` on every hop. Parameterize the differences (Accept header, media allowlist) and never fork the walk. When a header rule is relaxed, put the relaxing parameter on the inner function only: `writeHeadersStyle` has a single widened caller, `serveProxiedArtifact`.
