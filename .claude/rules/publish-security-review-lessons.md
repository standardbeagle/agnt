---
description: Durable lessons from the walkthrough-publish epic — why adversarial security review caught what green gates missed, content-digest cross-tenant leaks, defense-in-depth at parse boundaries, the recurring vendored-fork defect source, dead config, and epic-DoD scope narrowing.
source:
  - epic: 01KXBR3RX0QTSRGQYA733MDTZY
  - task: 01KXEDYRBTMCFM9DDSFTB1WX15 (P1 spec — CSP strip-merge rewind)
  - task: 01KXEDYRKFFPMBDJE1NEN9E43X (feedback/publish surfaces — cross-project digest-leak rewind)
  - task: 01KXEDYRFVNNPB1DD6FHF961TA (P4 public bundle — protocol-relative endpoint + substring-scan advisories)
  - task: 01KXFKH8DXA5KVCT2RNYMBB4WN (KDL feedback{} config wiring)
  - commits: a3340b5a d38205aa 8a01739a 97835086 8881179b 78b29b09 46491f5f 29466232 c50b6082 baf01b11 cb738b5f 7b79bba8 ec6b24cd
  - date: 2026-07-14
---

# Publish-epic security-review and scope lessons

## 1. A passing test suite is not a security proof — the adversarial reviewer earned its keep twice

Both rewinds on this epic happened on code that had already cleared its own
command gates (build/vet/unit tests all green) and the author's self-review.
The adversarial `security-review` / `correctness-review` persona is what
caught the actual boundary defect in both cases, because the author's own
tests proved the property they *intended* (share-id ownership, CSP header
presence) rather than the property that actually mattered (digest collision
behind the ownership check, strip-vs-replace semantics behind the header
check).

- **P1 spec** (task 01KXEDYRBTMCFM9DDSFTB1WX15): the spec claimed the public
  plane would wholesale-replace the upstream CSP, citing
  `stripFrameDenyHeaders`/`applyFrameHeaderPolicy` in `internal/proxy/rewrite.go`
  as precedent. Reviewer read the cited code: it's a strip-*merge* that
  deletes only `frame-ancestors` and explicitly preserves every other
  upstream directive. A hostile upstream's `script-src 'unsafe-inline'` would
  have survived onto the published artifact. Fixed by adding INV-12
  (`Header.Del` then `Header.Set`, never reuse the strip-merge path) plus an
  e2e assertion.
- **Feedback surfaces task** (01KXEDYRKFFPMBDJE1NEN9E43X): `ReadByRevision`
  keyed feedback reads by content digest, not share/owner. Two projects
  publishing byte-identical walkthrough content got the same digest and one
  project's viewer feedback leaked into the other's read. The existing test
  (`TestFeedbackReadOwnerScoped`) proved you can't *name* another project's
  share id — it never exercised the digest-collision path, so it stayed
  green through every author pass.

**Lesson**: for any change that introduces or touches a security/tenancy
boundary, treat "author tests pass" as necessary, not sufficient. Route it
through an adversarial reviewer whose job is to attack the boundary the
author's own tests assume rather than verify.

## 2. Content-addressing is a cross-tenant footgun when it doubles as a read key

Keying a read (or a union of records) by a content hash instead of an
owner/share identity silently unions across every owner whose content
happens to collide. This is invisible in single-tenant testing because
nobody publishes identical content twice in the same test — it only shows up
with a deliberate two-owner-same-content repro (which is exactly what closed
it: `TestFeedbackReadCollidingDigestNoCrossProjectLeak`).

**Rule**: content digests are fine as dedup/versioning keys. They are never
an access-control or read-scoping key. Scope every read by the owner/share
identity; use the digest only as opaque metadata on records already scoped
by owner.

## 3. Defense-in-depth beats a single CSP/allowlist layer at a parse boundary

Two P4 advisories on the public-bundle security gate (both non-blocking,
because CSP still neutralizes them) showed the same shape: a validator that
scans for literal forbidden substrings/protocols can be bypassed by
equivalent-but-differently-spelled input.

- `feedback-client.js`'s endpoint guard (`charAt(0) === '/'`) accepts a
  protocol-relative `//evil.com/collect`, which `fetch` resolves cross-origin
  — contradicts the code's own "can never point at a cross-origin sink"
  comment.
- `role_public_test.go`'s `forbiddenPublicTokens` is a literal substring
  scan on assembled bytes; string-split obfuscation (`window['__'+'devtool']`)
  would evade it (out of the *accidental*-inclusion threat model it targets,
  but worth naming).

Being "CSP-neutralized" is not the same as being closed at the boundary that
actually parses/validates the value. When a check is the stated security
control for a class of input, close it at that check (reject `//` prefixes;
resolve against `location.origin` and compare; or reject rather than scan
for a fixed decode), don't lean on an unrelated downstream layer to save it.

## 4. The vendored `github.com/standardbeagle/go-cli-server` fork is a recurring defect source

Three separate defects landed against this fork or its direct callers in the
walkthrough-publish timeframe: Windows build-tag drift
(`!windows`-only `hasReleasedResources` / `migrateLegacySocket`, see
`.claude/rules/platform-build-and-flake-lessons.md`), a sessionhost PTY
drain-race (task 01KXF2FZFQ), and a `ProcessManager` Start/Shutdown TOCTOU
(`wg.Add(1)` executed ~100 lines after the `shuttingDown` guard check, so
`Shutdown`'s `wg.Wait()` can return before `waitForProcess` registers).
Three independent hits on the same vendored dependency in one project
timeframe is a signal, not noise.

**Recommendation**: budget an upstream-fix pass on `go-cli-server` (submit
the fixes rather than re-patching the vendor copy each time), or reduce
surface-area reliance on the fork for process-lifecycle-critical paths.

## 5. Config that parses but doesn't drive behavior is worse than no config

The KDL `feedback{}` block existed in the schema and parsed cleanly, but sat
disconnected from the live public-plane rate limiter until task
01KXFKH8DXA5KVCT2RNYMBB4WN explicitly wired it through — before that, a
non-default value in the config file silently had zero runtime effect
(worse than a missing key, which at least fails loud or falls back
obviously; a *parsed-but-ignored* key looks configured and isn't).

**Rule**: any new config key needs a traced path to its consumer plus a test
that asserts a non-default value changes runtime behavior — parsing alone
is not done.

## 6. Verify the slice set covers the epic DoD's headline capability before execution starts

The epic's stated DoD was publishing/injecting over a **live external
production URL** ("publish against a real external URL... public visitor...
walks the step-through"). The decomposition (P1-P11) built a
self-contained-artifact publish instead — no task in the slice set carries
an upstream-URL field or proxies a live third-party origin at publish time.
This is a legitimate, defensible scope decision (self-contained artifacts
are simpler and safer to secure), but it silently narrowed the epic's
headline capability, and that narrowing only surfaced at epic close rather
than being flagged at plan time.

**Rule for planners**: when decomposing an epic, explicitly check the slice
set against the DoD's headline capability sentence. If the decomposition
narrows or reinterprets it, say so out loud in the plan (as a documented
scope decision with a reason), don't let it emerge as a surprise at
epic-close review.

**Closed** — the `pubserve` follow-on epic (`01KYJBPVCX5BKD7YS03E4MTX87`)
carried the narrowed capability back: slice S6
(`01KYJC0CQTMCV91FZ5B251V50S`, commits `f61821ae`/`85e6b409`) made the public
plane actually reverse-proxy a share's live `Upstream` origin. The rule above
still stands as a plan-time check; the point is that the narrowing was
recoverable *because it was written down*, not that it was harmless.

## 7. Testing a correct SSRF/deny-list guard end to end is self-defeating unless the seam verifies the control

A deny-list that correctly refuses loopback, RFC1918 and link-local space
refuses **every address a Go test can actually bind**. `httptest.NewTLSServer`
listens on 127.0.0.1, which is exactly what
`publish.CheckUpstreamOrigin` exists to deny. The naive outcomes are both
bad: the test fails against correct code, or the guard is disabled/widened
"for tests" — which deletes the coverage the test was written to provide.

The shape that worked (S6, `internal/proxy/public_routes_test.go`
`testUpstream`, guarding `guardedUpstreamFetcher` in
`internal/proxy/public_routes.go`):

- The production type carries three seams — `resolve` (a
  `publish.Resolver`), `dial`, and `tlsConfig` — whose **zero value is the
  production path**, so no build tag and no test-only branch inside the
  guard.
- The test resolver answers with a genuinely **public** address
  (`93.184.216.34`). The guard therefore runs its **real** logic and passes
  on its own merits; nothing about the deny table is stubbed out.
- The test **dialer asserts it was handed exactly that address** before
  redirecting the connection to the local listener. The pinned address is
  the assertion, not the bypass — a regression that re-resolved the hostname
  would hand the dialer a different address and fail.
- The URL hostname stays `example.com` (the name httptest's cert is issued
  for), so TLS verification is real rather than skipped — the seam does not
  quietly cost a second control.

**Generalize**: for any allowlist/denylist guard whose deny set includes the
only addresses the test environment can bind, inject the resolve/dial seam
and make the test **assert on what the seam received**. A seam that removes
the control tests nothing; a seam that *observes* the control turns the
untestable end-to-end path into the strongest available assertion. Reuse
`guardedUpstreamFetcher`'s shape rather than re-deriving a URL-validating-only
variant — a guard that validates a hostname and then lets
`http.Transport` re-resolve it is decorative (DNS rebinding answers publicly
for the check and privately for the dial).

## 8. "It errored" and "the control refused it" are different assertions — assert the refusal's provenance

`TestGuardedFetchRechecksEveryRedirectHop` covers the canonical SSRF bypass:
a public origin returning `302 -> https://169.254.169.254/`. A bare
`if err == nil { t.Fatal(...) }` has **no teeth here**, because with the
per-hop guard deleted the fetch would *still* error — the harness's own
pinned-dial assertion (§7) rejects the unexpected address and produces an
error of its own. A passing `err != nil` would prove nothing about the guard.

Two assertions carry the actual meaning:

1. the error text names the guard's **verdict** (`"upstream refused"`), so
   the refusal is attributable to `CheckUpstreamOrigin` and not to an
   incidental downstream failure; and
2. `len(*dialed) == 1` — the forbidden hop **never reached the dialer at
   all**, which is the property that matters (refusal must precede the
   socket, not follow it).

**Generalize**: when a test asserts that a security control *refused*
something, assert the **provenance** of the refusal, never merely that the
operation failed. Ask explicitly: "if I deleted this control, would this
assertion still pass?" — in any layered path (harness checks, transport
errors, downstream validation) the answer is usually yes, and the test is
theatre. Prefer (a) matching the control's own verdict, and (b) asserting the
dangerous side effect was never reached. This is the same family as
`.claude/rules/lessons-liveness-probes.md`: a probe/assertion that cannot
distinguish "the thing I care about happened" from "something unrelated
went wrong" is a lie about coverage, whichever direction it points.

## 9. A criterion that names a test tier must ship in a fileScope that can reach it

S6's acceptance criteria demanded "real-Chrome assertion is chromee2e-tagged",
but its `fileScope` was
`{public_routes.go, public_routes_test.go, rewrite.go, rewrite_test.go}` —
excluding `internal/proxy/publish_browser_e2e_test.go`, the only file that
tier lives in (see `AGENTS.md` § Testing, Browser E2E loud-skip policy). The
criterion was **unsatisfiable inside the scope it shipped with**. The
implementer correctly refused to edit out of scope rather than silently
widening it, and the reviewer classified the gap as a **planning defect, not
an execution one** (accepted + follow-up slice).

**Rule for planners**: a criterion naming a test tier, a config file, a doc,
or a generated asset must have the owning file inside the task's `fileScope`
— otherwise the criterion belongs to a different slice. This is §6's
check one level down: §6 asks whether the *slice set* covers the DoD; §9
asks whether each *slice* can satisfy its own criteria without breaking
scope. Both failures look identical from inside execution — an implementer
doing the right thing and still "failing" a criterion.

Corollary from the same review: prefer criteria phrased as "X is verified
against the code path, not only via a passing test." Both of this epic's
rewinds landed on green suites (§1), and the value in S6's review came from
reading `CheckUpstreamOrigin`'s return contract and `writeHeaders`'
`Del`/`Set` ordering directly.

<!-- provenance: §7-§9 and the §6 closure note —
     written_at 2026-07-29;
     source_event task S6 01KYJC0CQTMCV91FZ5B251V50S
       (epic 01KYJBPVCX5BKD7YS03E4MTX87), verdict pass, 0 blockers,
       attempt 1, no rewinds;
     commits f61821ae, 85e6b409 -->

## See also

- `.claude/rules/platform-build-and-flake-lessons.md` — the vendored-fork
  build-tag-drift and flake-misdiagnosis lessons this doc's §4 extends with
  a third (TOCTOU) instance.
- `.claude/rules/lessons-liveness-probes.md` — the "signal with no teeth"
  family §8 belongs to (an indefinite-blocking liveness probe that can never
  fire is the same defect as an assertion that can never fail).
