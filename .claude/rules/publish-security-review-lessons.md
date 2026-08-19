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
  - epic: 01KYJBPVCX5BKD7YS03E4MTX87 (pubserve follow-on — §6 closure, §7-§14)
  - task: 01KYJC0CQTMCV91FZ5B251V50S (S6 live upstream — §7-§9)
  - task: 01KYJC0CWY6DBN91V86CMDYFRX (S7 demo indicator — §13, §14; re-scope)
  - task: 01KYJC0CZVVC0TTYT720KWYSYX (S8 share store — §8 extension, §12)
  - task: 01KYJC0D3FDSG4A05W9058YNR0 (S9 publish serve — §12)
  - task: 01KYJC0D6K1NAA9Q9J68T4CW7B (S10 operator doc — §5 extension, §10, §11; only rewind)
  - date: 2026-07-29
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

### Extension: generalize from "config key" to "any validated input"

The `pubserve` epic hit this class again, on an **op field** rather than a
config key, and in a worse form — the validator is *partial*, so the author
gets a success signal. `addScript`'s `src` (`internal/publish/op.go:159-166`)
is validated for scheme and length via `ValidateURL`, publishes cleanly, and
reports success. Nothing ever fetches it: `grep '\.Src'` across
`internal/publish`, `internal/proxy`, `internal/tools`, `cmd` yields only
`op.go` itself (validate / `forbidExcept` / signature). At render time
`internal/proxy/scripts/variant-engine.js:414` refuses the op outright
(`"addScript: src must be inlined at publish time; the renderer emits no
<script src>"`), so the authored script silently never runs. Filed as
`01KYQJ9ARWQ0YMGWZ199B6SA3F`.

**Generalized rule**: the defect class is *any input a boundary validates
but no consumer reads* — config key, op field, CLI flag (cf.
`.claude/rules/lessons-ssh-transport.md` #4's inert `--tool`), or wire
field. A **partial** validator is worse than no validator: it converts
"unsupported" into "accepted, then silently dropped." Pending the real
consumer, the honest shape is a hard reject at validation time with a
"not implemented" reason — not a doc warning, and not a scheme check that
implies the rest of the pipeline exists.

Corollary sighted by the same review: introducing a boundary validator does
**not** retire the raw casts that predate it. `tunnel.ParseProvider` landed
in S9 while `internal/daemon/hub_tunnel.go:55` still casts a config string
straight to `tunnel.Provider`. Sweep the existing call sites in the same
epic, or the validator is decorative everywhere except its newest caller.

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

### Extension: stop asking and start breaking — mutation-verify the guard

§8 poses the question ("if I deleted this control, would this assertion still
pass?"). S8 (`01KYJC0CZVVC0TTYT720KWYSYX`) shows the cheap way to *answer* it
instead of reasoning about it. Reviewing the cross-tenant collision test for
the share store, the reviewer did not accept it on its word: it **rewrote
`sourceKey` to return `fileName` only**, re-ran, and confirmed
`TestPublishFileIdenticalContentDistinctOwners` failed on the *behavioural*
assertion (share ids collapsed, `share_store_test.go:613`) **before** reaching
the tautological `sourceKey(a) != sourceKey(b)` line — then restored the tree.
That ordering matters: a test that only fails on the key-comparison line proves
the key changed, not that the tenancy boundary held.

The test itself carries the other half — it asserts its own **premise** first:

```go
// Test premise: the two shares really do collide on content digest.
if infoA.Digest != infoB.Digest {
    t.Fatalf("premise broken: digests differ (%q vs %q), the collision path is untested", ...)
}
```

Without that check the test degrades silently into exactly the blind spot §2
records — the original digest-collision defect survived every author pass
because no test published identical content twice.

**Generalize**: (a) a regression test for a *known* security defect should be
mutation-verified once, at review time, by reverting the fix and confirming the
test fails on a behavioural assertion — this is a minute of work and it is the
only direct evidence the test has teeth; and (b) any test whose value depends
on an input **coincidence** (equal digests, colliding keys, equal hashes, same
timestamp) must assert that coincidence as a named premise, or it will pass
vacuously the moment the coincidence stops holding.

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

## 10. A doc that claims to describe shipped behaviour is a security surface

S10 (`01KYJC0D6K1NAA9Q9J68T4CW7B`) was a **docs-only** slice and it is this
epic's only rewind. The reviewer blocked it, correctly, on one sentence in
`docs/public-walkthroughs.md` §9a:

> "The same guard covers publish-time `addScript src` fetches, not just the
> upstream document."

No such fetch exists. `publish.CheckUpstreamOrigin` has exactly **one**
production caller — `internal/proxy/public_routes.go:709`, the guarded
upstream-document fetch and its redirect walk — and nothing reads `op.Src`
at all (see §5's extension). Because the doc's own header says it summarizes
what ships today, the sentence read as a **live security control**: a false
assurance an operator would rely on when deciding what an authored
walkthrough is allowed to reference.

**Rule**: a doc sentence asserting a security control's *scope* is a
load-bearing claim and must be verified exactly as if it were code. The check
is cheap and mechanical: **grep the guard function's production callers and
confirm the doc's coverage claim matches the call sites you find.** The count
is usually one. A doc that trades stale claims for new false ones is worse
than the stale doc, because someone acts on it.

**The reliable tell is coverage-quantifier language.** Both of this doc's
false claims, and both of the implementer's own sweep fixes, were the same
shape — a control described with broader reach than its single call site has.
"Every outbound fetch is therefore guarded" was *literally* true (only one
fetch exists) yet read as a policy that would survive the next fetch someone
adds. Treat "every", "all", "the same guard also" in a security doc as a
prompt to go count call sites.

Related, from the same review: CSP-adjacent behaviour in this codebase is
routinely enforced at **two** layers (renderer/validator first, then the CSP
header), and prose reliably credits the wrong one. State which layer refuses
a thing *first*, and whether the other is primary or defence-in-depth.

### 10a. One instance of a claim-shape defect obliges a whole-file sweep

The reviewer itemized the class twice. The attempt-2 implementer's own
post-edit sweep (grep the file for `guard`/`SSRF`/`src`/`pipeline`) then found
**two more** instances the reviewer had not named: §9a's "Every outbound fetch
is therefore guarded", and §3's `variantSet` bullet implying an authored
script runs when a `src` op silently never does.

**Rule**: when a review finds one instance of a claim-shape defect, the fix
must include a whole-file (or whole-doc) sweep for that *shape*, not just the
cited line. Fixing only the cited instance is actively harmful: the siblings
survive into a file that now carries a passing review, so they read as
newly-blessed by the review that missed them.

Note also that the honest fix was **not** deleting the false sentence. The
falsehood was concealing a real silent-failure mode; deleting it would have
removed the lie and left the trap undocumented. The shape that passed was
*name the gap, name the silent failure, cite the tracking task id*.

## 11. A dispatch brief or a reviewer's `fix_hint` is a hypothesis, not an authority

Attempt 1's blocker asserted the `addScript src` op was stopped "at render
time by the hash-only `script-src`", and the coordinator's dispatch brief
repeated it. Both were wrong about the mechanism. The implementer read the
code and followed the code: `variant-engine.js:414` refuses the op
**during validation**, so no `<script>` is ever emitted and CSP never
adjudicates at all; `authoredScriptCSPHashes` skipping a `Src`-carrying op
(`public_routes.go:544`) is belt-and-braces, as that code's own comment says.
The re-reviewer independently confirmed the implementer was right and that
attempt 1's `fix_hint` had been imprecise.

Had the brief been transcribed, the fix would have **replaced one false
mechanism claim with another, inside the very paragraph the blocker existed to
make accurate.** The same slice also corrected the coordinator's account of
`--tunnel`'s accepted values, the CSP's `script-src` composition, the SSRF
deny-list's breadth, and the upstream fetch bounds — four more instances in
one task.

**Rule, both directions**:

- *Dispatching:* brief a subagent with your understanding explicitly marked as
  a hypothesis, and instruct it to verify against code and **report
  contradictions**. A brief treated as ground truth propagates the
  coordinator's errors into artifacts, where they outlive the conversation.
- *Reworking:* a `fix_hint` containing a causal or mechanism claim is a
  diagnosis, not a specification. Verify it, follow the code, and **record the
  divergence in the commit message** so the next reviewer can audit the
  disagreement instead of flagging it as an unrequested change.
- *Reviewing:* verify the mechanism before writing the `fix_hint`. An
  imprecise hint on a docs slice costs a full rewind cycle and risks the fix
  encoding the reviewer's own error.

## 12. Make the dangerous input unrepresentable, not merely checked

`publish.Store.ReconcileFiles(projectPath, presentFiles)` revokes every
file-backed share of a project whose source file is absent from the set it is
handed — **irreversibly**, by design (INV-4: a resurrected token silently
re-opens a URL the owner deliberately killed). Its hazard is that a *failed*
enumeration is spelled the same as an *empty* one: a transient scan failure
destroys every live link.

S8's reviewer flagged this as a hazard for the **future** consumer. S9 then
closed it **structurally** rather than with a precondition check:

- `loadWalkthroughDir` returns a complete set **or** an error — one bad
  `*.json` aborts the whole pass, naming the file. A partial set is
  *unrepresentable*, so there is no call site at which to forget the check.
- The reconcile is reached only after every file published successfully; any
  early return skips it entirely. Exactly **one** production call site
  (`cmd/agnt/publish_serve.go:262`), trivially auditable.
- `dirFingerprint` returns an error and never folds a read failure into an
  empty fingerprint; a failed pass does **not** advance `last`, so the next
  tick is a free retry with no retry machinery. The code says so out loud:
  *"Do NOT treat an unreadable directory as 'everything was deleted': that is
  the mass-revoke trap."*

**Rule**: for any irreversible batch operation driven by a diff against an
enumerated set (mass delete, revoke, tombstone, cache purge, GC), ask **"can a
failed enumeration be spelled as an empty enumeration?"** If yes, fix the
*loader's return type*, not the caller. A "check the set is complete first"
guard is a rule every future caller must remember; an unrepresentable partial
set is a rule the compiler enforces.

**Corollary for reviewers**: a hazard noticed for a not-yet-written consumer
should be handed **forward as an explicit constraint on that consumer's task**,
not left in a closed review. S8's reviewer did exactly that ("S9 wiring must
never call `ReconcileFiles` with the result of a failed or partial directory
scan"), which is why S9 solved it structurally on attempt 1.

**Corollary for planners**: state such a criterion as *"the destructive call is
unreachable from a partial/failed scan, verified by reading every call site"*
rather than *"a test proves shares survive a failed scan."* The structural
property is what holds under future edits — and both of this epic family's
rewinds landed on green suites (§1).

**Residual, still open**: an unmounted-yet-present directory reads as empty,
which is indistinguishable from "all files deleted." Filed for a two-poll
confirmation.

## 13. A slice's whole flow can be unsatisfiable, not just one criterion

This is §9 recurring one level up. §9 says a *criterion* must be satisfiable
inside its own `fileScope`. S7 (`01KYJC0CWY6DBN91V86CMDYFRX`) shows a slice's
entire **flow** can describe an architecture that does not exist.

S7 as planned said "render the indicator in the chrome shell, outside the
content iframe", scoped to `injector.go` + `scripts/indicator-styles.js`. All
three premises were false in code:

1. S6 deliberately does **not** wrap the upstream — the upstream document *is*
   the artifact document, so there is no shell frame on the public plane.
2. `indicator-styles.js` is a **`RoleChrome`** module (`embed.go:432`) and
   `rolePublicModules` (`embed.go:465`) is a **closed allowlist**, so editing
   it cannot affect any public response.
3. `injector.go`'s `BuildShellDocument` is dev-only (it emits
   `__devtool_role="chrome"` and the `RoleChrome` asset); its own comment says
   adding a role parameter would be an INV-1 regression.

The implementer **stopped** and returned an out-of-scope report with a
recommended re-scope instead of widening scope or faking the outcome. The spec
(§9c/INV-14 plus the §11 traceability table) had been right all along — the
*task* had mis-mapped the dev-plane wrap model onto the public plane at plan
time.

**Rule for planners**: before writing criteria, verify a slice's named
integration points **exist and are on the path the slice claims**. Concretely
for this codebase: cross-check `moduleRole` / `rolePublicModules` membership at
plan time — a `fileScope` naming a `RoleChrome` module for a `RolePublic`
outcome is a planning defect that no amount of execution can fix.

**Rule for implementers**: the correct response to an impossible slice is a
**STOP plus a re-scope report**, never a silent scope widening and never a
plausible-looking substitute. A `pass` verdict on a re-scoped task is then
judged against the *rewritten* task content, not the original framing.

**Rule for resolving apparent conflicts**: the spec's traceability table was
the tie-breaker. An apparent conflict between INV-14's tamper language and a
user-made decision ("the module is not adversarially non-removable")
dissolved once the table showed the module is P4 while the adversarial tamper
e2e is P10 — the two were never actually in conflict. Consult the traceability
mapping before escalating a spec-vs-decision contradiction.

## 14. CSSOM is the CSP-compatible seam for injecting styled UI

Verified end to end on S7. The public plane pins `script-src` to a **hash**
and passes an **empty** nonce on the proxied path
(`public_routes.go:326`: *"No nonce: ... A nonce source nothing in the
document carries would authorise nothing"*), so the response authorises **no
inline style whatsoever** — not even the upstream's own inline `<style>`
blocks or `style=` attributes.

`demo-indicator.js` styles a mandatory disclosure badge under that CSP with
zero directive widening:

- A constructed `CSSStyleSheet` + `replaceSync` (with an `insertRule`
  fallback) assigned to `root.adoptedStyleSheets` on a **closed** shadow root.
  **CSSOM-inserted rules are not subject to `style-src`.**
- Zero inline `<style>`, zero `style=` attribute, zero `el.style`/`cssText`
  writes (verified: the module contains none).
- `:host { all: initial !important; … !important }` so page CSS cannot win the
  cascade — the cheap, real half of tamper resistance.

The second half is why **no header edit was needed at all**: `cspHash` is
`cspSHA256(publicInstrumentationAssetBytes())` (`injector.go:115`) and serves
as *both* the CSP `script-src` hash and the `<script>` SRI `integrity`
attribute (`public_routes.go:141`, `:387`), while `assetPath` is
content-addressed over the same bytes. **The pin is a function of content, so
content growth re-pins itself.**

**Rule**: (a) to inject styled UI the plane renders itself (not authored
content) into a strict-CSP page, use CSSOM into a closed shadow root, paired
with `:host{all:initial!important}`; and (b) **before proposing a CSP change
for a new bundle member, check whether the pin is content-derived — if it is, a
header edit is a red flag, not a requirement.** Every instinct on this epic was
to widen a directive; the correct move was to verify the derivation and change
nothing.

Two honest limits, both in the shipped docs: the badge degrades to *unstyled*
(disclosure text survives, styling skipped) on a platform without constructable
stylesheets rather than failing loud; and a benign SPA upstream doing
`document.body.innerHTML = …` on hydrate silently removes a one-shot appended
element. That second one is a **distinct threat model from adversarial
tampering** — P10's "a variant cannot hide it" e2e covers hostile removal and
would not catch incidental removal. Split any always-on disclosure overlay's
robustness into hostile-removal and incidental-removal, and assign each
somewhere.

<!-- provenance: §10-§14, and the extensions to §5 and §8 —
     written_at 2026-07-29;
     source_event pubserve epic 01KYJBPVCX5BKD7YS03E4MTX87 tail:
       S7  01KYJC0CWY6DBN91V86CMDYFRX (commits 74c78aa4, e9578c12;
           verdict pass attempt 1, after a re-scope: all three planned
           premises were false in code);
       S8  01KYJC0CZVVC0TTYT720KWYSYX (commits 7b9f2288, b2991907;
           verdict pass attempt 1);
       S9  01KYJC0D3FDSG4A05W9058YNR0 (commits 5099f44b, 24118a96,
           4850dbf2, b7b9f237, 0940b58e; verdict pass attempt 1);
       S10 01KYJC0D6K1NAA9Q9J68T4CW7B (commits 73b7606a, 2c736d31,
           9aa2e86b; the epic's ONLY rewind — 1 major blocker on a
           docs-only slice, fail attempt 1 -> pass attempt 2);
     follow-ups filed: 01KYQJ9ARWQ0YMGWZ199B6SA3F (addScript src has no
       consumer), 01KYQEGBK1XWS4TKVCYCR6YYFY (test writes into the source
       tree) -->

<!-- provenance: §7-§9 and the §6 closure note —
     written_at 2026-07-29;
     source_event task S6 01KYJC0CQTMCV91FZ5B251V50S
       (epic 01KYJBPVCX5BKD7YS03E4MTX87), verdict pass, 0 blockers,
       attempt 1, no rewinds;
     commits f61821ae, 85e6b409 -->

## 15. A reverse-proxy header rewrite is a CSRF trust boundary — rewrite only the value the proxy manufactured, never blanket

A browser served from agnt's dev proxy (backend fronted on a hash-derived
port 10000-60000) sends `Origin: http://localhost:<proxy-port>`, which the
`ReverseProxy` Director forwarded verbatim to a backend whose CORS allowlist
only knows its own origin — so the backend's CORS middleware logged a
mismatch on every request. Harmless (the browser sees same-origin on the
proxy port, so the CORS decision never mattered), but it was agnt's cosmetic
noise in the user's log (external report from the bifrost project). The
tempting fix — have the Director rewrite `Origin` to the backend's own origin
— is a **CSRF hole if applied unconditionally**: a genuinely cross-site
request would then look same-origin to the backend, silently defeating its
Origin/CSRF checks.

The shape that shipped (`internal/proxy/server.go` Director,
`origin_rewrite_test.go`, commit `cdd832b9`):

1. **Rewrite iff the inbound Origin is the origin agnt itself introduced.**
   The guard is host-equality against the proxy's own request `Host` captured
   **before** the Director runs (`originalHost`): rewrite only when
   `url.Parse(Origin).Host == originalHost`. For a same-origin browser
   request the two are equal because both derive from the one navigated proxy
   URL — robust across `localhost`/`127.0.0.1` naming since both sides come
   from that single URL. A third-party Origin and an absent Origin are
   forwarded **verbatim**.
2. **The regression test asserts the exact forwarded value the backend
   captured** (`got != thirdParty`), not merely that the request succeeded —
   the strongest provenance form (§8). It is **mutation-verified**: reverting
   the scope guard to a blanket rewrite makes the backend see the backend
   origin instead of `evil.example.com`, and the CSRF-safety assertion fails
   as required — proving it is not tautological (§8 extension).

**Generalize**: any reverse-proxy rewrite of a header a backend security
control keys on — `Origin`, `Referer`, `Host` — must rewrite only the value
the proxy manufactured (matched against the pre-Director `req.Host`), never
blanket. Blanket-rewriting the class of header that a downstream control
trusts converts "cosmetic cleanup" into "silently disarm the backend's
cross-origin defense." This is the §12 shape ("make the dangerous input
unrepresentable") applied to an *outbound* header: the dangerous case
(cross-site Origin laundered to same-origin) is never constructible because
the guard fires only on the one origin the proxy owns.

**Corollary — trace the dispatch path before trusting a "may weaken X"
hazard.** The concern that this rewrite could weaken agnt's own
control-channel WS origin check (`checkWSOrigin`) was dispelled by reading the
wiring, not by adding a defensive fallback: `checkWSOrigin` is wired only into
`ps.wsUpgrader` for the `/__devtool_metrics` path, served by `handleWebSocket`,
which **never invokes the Director** — so it still evaluates the browser's
original un-rewritten Origin. A Director header change is structurally unable
to reach it. (Backend WS upgrades *do* run the Director, but the scoped
rewrite is consistent there too: proxy-own origin → same-origin, third-party
WS Origin preserved.) Same family as §11: verify the mechanism against the
code before hardening against a hazard that may not exist on the real path.

<!-- provenance: §15 —
     written_at 2026-08-18;
     source_event task 01KYZ0H4HB21ZPQ6KGC9KVG4MN (workspace agnt),
       verdict pass, 0 blockers, attempt 1, no rewinds;
     commit cdd832b9 -->

## See also

- `.claude/rules/platform-build-and-flake-lessons.md` — the vendored-fork
  build-tag-drift and flake-misdiagnosis lessons this doc's §4 extends with
  a third (TOCTOU) instance.
- `.claude/rules/lessons-liveness-probes.md` — the "signal with no teeth"
  family §8 belongs to (an indefinite-blocking liveness probe that can never
  fire is the same defect as an assertion that can never fail).
