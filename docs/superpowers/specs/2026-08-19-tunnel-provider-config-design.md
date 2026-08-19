# Tunnel provider config surface — design

- **Status**: draft, design-only (no implementation authorized)
- **Task**: `01KYXEYF6WEXWHCXXYRWQTZN56` — "Tunnel provider has no config surface — but a flat default is likely the wrong shape"
- **Date**: 2026-08-19
- **Scope**: whether `.agnt.kdl` should carry any tunnel-provider configuration, and if so, what shape

## 1. Current state (verified against code)

- `internal/tunnel/tunnel.go` supports exactly three providers: `cloudflare`
  (cloudflared Quick Tunnel), `ngrok`, and `tailscale` (`tailscale serve`,
  tailnet-private HTTPS at the node's MagicDNS name). The closed set lives in
  `supportedProviders`; `ParseProvider` (added in pubserve S9, commit
  `5099f44b`) is the boundary validator that fails loud naming the legal set.
- **There is no tunnel configuration surface.** `grep -rn tunnel
  internal/config/` finds only `PublicURL` (`internal/config/agnt.go:540`) and
  a comment in `publicplane.go`. No `.agnt.kdl` key selects, prefers, or
  restricts a provider.
- Every start path requires the provider to be named explicitly, and every
  path validates through `ParseProvider`:
  - MCP `tunnel {action:"start"}` — `internal/tools/tunnel_tools.go:124`
    rejects an empty provider before the wire call.
  - Daemon `TUNNEL START` — `internal/daemon/hub_tunnel.go:44` rejects empty,
    then `:56` calls `tunnel.ParseProvider`. (The raw
    `tunnel.Provider(configString)` cast this task originally flagged at
    `hub_tunnel.go:55` no longer exists — the boundary-validation cleanup
    landed in commit `7cc34fa5`.)
  - `agnt publish serve --tunnel` — `cmd/agnt/publish_serve.go:485` parses
    before binding anything, "so a typo fails immediately".
- Posture facts (AGNTS.md § Exposure Posture, operator decision 2026-07-31):
  cloudflare and ngrok are **genuinely public**; tailscale is
  **tailnet-private** (authenticated, encrypted, device-scoped). Shipped
  default for every listener is loopback / no tunnel. *Widening a posture is a
  user decision, never an agent's — and, this spec argues, never a config
  file's.*
- Operator usage (recorded 2026-08-01, weakening the task's original premise):
  the owner uses **both** providers for **different purposes** — cloudflare
  for public shares, tailscale for VPN-style access. There is no single
  habitual provider.

## 2. Why a flat `tunnel { provider "…" }` default is the wrong shape

Provider selection is not a preference; it is an **exposure-posture
decision**. `--tunnel cloudflare` and `--tunnel tailscale` differ in who on
Earth can reach the listener, not in vendor taste. A remembered flat default
breaks in three distinct ways:

1. **Config silently decides exposure.** With a stored default, `tunnel
   {action:"start", id:"dev", local_port:8080}` — no provider named — would
   raise whatever the file says. If the file says `cloudflare`, an invocation
   that *looks* posture-neutral publishes a dev server to the open internet.
   That is precisely the "widening by default instead of by decision" the
   Exposure Posture section forbids, moved from install-time to config-time.
2. **It serves a user who does not exist here.** A flat default helps someone
   who always wants the same provider. The actual recorded usage is
   per-purpose: public share → cloudflare, own-device access → tailscale.
   For that user, naming the provider at the call site *is the decision being
   expressed*, not friction to remove. A default would be wrong roughly half
   the time — and wrong in the dangerous direction whenever the stored value
   is a public provider.
3. **It hands the decision to the wrong principal.** `.agnt.kdl` is
   project-checked-in, agent-editable state. An AI agent can already start a
   tunnel via the MCP tool; today it must *say* `cloudflare` in the call,
   which is auditable and matches `lessons-ssh-transport.md` §7 (scripted
   paths need explicit opt-in consent, never an inherited default). A config
   default would let a past edit — human or agent — pre-authorize a future
   public exposure that no one re-decides.

## 3. Option space

### (a) Status quo: per-invocation explicit provider, no config key

- **Exposure semantics**: every tunnel start names its posture at the call
  site. No stored state can widen exposure.
- **Principle fit**: perfect. This is the config-shaped analogue of the
  bootstrap-consent rule (`lessons-ssh-transport.md` §7).
- **KDL shape**: none.
- **Cost**: typing one word per invocation; agents must know the provider set
  (the MCP tool's jsonschema and error text already enumerate it).

### (b) `.agnt.kdl` records a preferred provider, but every start still confirms

E.g. `tunnel { prefer "tailscale" }` used only to pre-fill an interactive
prompt or annotate `tunnel {action:"list"}` output; non-interactive starts
still require the explicit provider.

- **Exposure semantics**: safe only if the "still confirms" half is enforced
  on every path — which makes the key almost inert: the daemon and MCP paths
  are non-interactive, so the preference could legally influence nothing
  there. A key whose value changes behavior on no shipped path is exactly the
  parsed-but-inert defect class
  (`publish-security-review-lessons.md` §5). The moment someone "fixes" the
  friction by letting the preference satisfy the requirement, it degrades
  into option (d)/flat default.
- **KDL shape**: `tunnel { prefer "tailscale" }`.
- **Verdict**: unstable shape — either inert or a default in disguise.

### (c) Posture-typed restriction: config narrows the allowed set, never selects

E.g.:

```kdl
tunnel {
    allow "tailscale"           // this project may only raise tailnet-private tunnels
}
```

- **Exposure semantics**: strictly narrowing. The caller still names the
  provider explicitly; config can only *refuse* (`cloudflare` in a project
  whose owner declared tailscale-only fails loud at the same boundary
  `ParseProvider` guards). Absent block = current behavior (all three
  allowed), so the zero value cannot widen anything.
- **Principle fit**: good. A restriction key cannot cause exposure; it can
  only prevent it. It is also the only option that adds a capability the CLI
  cannot already express: a project-level "never public" guarantee that holds
  against habit, typo, and agent autonomy alike.
- **KDL shape**: repeated `allow` nodes (KDL convention for sets), validated
  through `ParseProvider` at parse time so a typo in config fails loud, and
  enforced at the single chokepoint where all three start paths converge
  (`hub_tunnel.go` for daemon paths; `publish_serve.go` checks the same
  helper).
- **Cost**: a new key that must satisfy Config Authority (§5 below); a fourth
  enforcement consideration when a future provider is added.

### (d) Layered: tailnet-private providers configurable as default, public ones never

E.g. config may store `default "tailscale"` (posture-safe: tailnet-private),
but storing `cloudflare`/`ngrok` as a default is rejected at parse time.

- **Exposure semantics**: cannot silently widen to public — the dangerous
  half is unrepresentable. But it still makes *some* invocations
  posture-implicit, and it bakes a posture taxonomy ("which providers are
  default-safe") into the config parser that must be re-adjudicated per new
  provider. Tailnet-private is private to the *tailnet*, not to the machine:
  defaulting it still widens loopback → tailnet without a per-call decision.
- **KDL shape**: `tunnel { default "tailscale" }` with a parse-time posture
  check.
- **Verdict**: defensible but over-built for the recorded usage (the
  two-provider user gains nothing from a tailscale default they'd override
  half the time), and it violates the cleaner rule that *no* stored value
  selects a provider.

## 4. Recommendation

**Adopt (a): close the task as won't-do for any provider-selecting config
key.** Explicit `--tunnel <provider>` / `provider:` per invocation is the
correct shape because the provider name *is* the exposure decision, and this
repo's standing rule is that exposure decisions are made fresh by the user,
never replayed from stored state. The one recorded user chooses per purpose,
so a default has no beneficiary.

**If a config surface is ever demanded, (c) — the `allow` restriction — is
the only acceptable shape.** It is the unique option that is monotonically
narrowing: config can forbid a posture but can never cause one. (b) is inert
or degenerate; (d) still converts a stored value into an exposure widening.
Do not build (c) speculatively now — there is no request for it, and
`dev-standards:ponytail` / OVER-ENGINEERING FORBIDDEN both say a guard nobody
asked for is scope creep. This spec exists so the next person who reaches for
`tunnel { provider … }` lands on `allow` instead.

## 5. Config Authority requirements (binding on any future implementation of (c))

Per `.claude/rules/daemon-architecture.md` § Config Authority and
`publish-security-review-lessons.md` §5 (parsed-but-inert keys are worse than
no keys — this repo has hit that class repeatedly, most recently the KDL
`feedback{}` block):

1. **Traced consumer.** The `allow` set must be threaded to a single
   enforcement chokepoint that all three start paths reach: the daemon
   `TUNNEL START` handler (covers the MCP tool transitively) and
   `publish serve --tunnel`'s parse site. No path may bypass it.
2. **Behavioral test.** A test must prove a non-default value changes what
   runs: with `allow "tailscale"`, starting `cloudflare` fails loud naming
   both the refused provider and the config source; with no `tunnel` block,
   all three start. Parsing alone is not done.
3. **Reuse `ParseProvider`.** Config values validate through
   `tunnel.ParseProvider` at load time — never a raw `tunnel.Provider(s)`
   cast (the class `7cc34fa5` just cleaned out of `hub_tunnel.go`).
4. **Zero value is current behavior.** Absent block ⇒ all providers allowed;
   an *empty* `tunnel { }` block must not be spelled the same as "deny all"
   (the mass-revoke trap shape, `publish-security-review-lessons.md` §12 —
   decide explicitly which it means and test it).
5. **Never starts a tunnel.** No config key may cause a tunnel to be raised.
   Config selects nothing and restricts only; the shipped default remains
   loopback / no tunnel. Test that a fully-populated `tunnel` block with no
   explicit start request raises nothing.
6. **Docs state posture per provider.** Any documentation of the key must
   restate that cloudflare/ngrok are public and tailscale is tailnet-private,
   per AGENTS.md § Exposure Posture.

## 6. Open questions for the owner

1. **Close as won't-do?** Does the recommendation to keep explicit
   per-invocation selection (and file nothing) match your intent, or do you
   want the (c) `allow` restriction built now for specific projects you'd
   like pinned tailnet-only?
2. **Empty-block semantics** (only if (c) is ever built): should
   `tunnel { }` with no `allow` nodes mean "allow all" (treat same as absent)
   or "deny all" (an explicit lockdown)? §5.4 requires this decided and
   tested, not defaulted into.
3. **Does `publish serve` need the restriction too, or daemon-only?** It runs
   without the daemon; enforcing (c) there means loading `.agnt.kdl` from the
   served project, which `publish serve` does not currently do at all.
4. **Task metadata**: the task carries tag `bugfix`; if closed won't-do or
   re-shaped into a future (c) task, the tag should change (`design` /
   `hardening`) so loop tooling doesn't route it to a fix template.
