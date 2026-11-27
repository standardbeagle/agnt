# Self-Hosted Licensing — Design

Date: 2026-06-08
Status: Approved (approach A)

## Goal

Gate Pro features of `agnt` behind a self-hosted, offline-validatable license.
No monthly platform cost, no runtime phone-home.

- **License format**: `hyperboloide/lk` — ECDSA-signed (P-384) base32 blob
  wrapping a JSON payload. One dependency, purpose-built for licensing.
- **Validation**: offline, embedded public key only. Private key never ships.
- **Payments**: Stripe (server-side, **out of scope for this task** — see Seam).
- **Enforcement**: Pro features hard-block past a 14-day grace window; core
  features always free.

## Scope decision: what is "Pro"

The gate line is **breadth of operation**, not a static tool list:

- **Free**: single-page operations — current `responsive_audit`, `api_audit`,
  `loading_audit`, accessibility, snapshot, browser debug, process/proxy mgmt.
- **Pro**: whole-site / multi-page / application-wide operations — advanced
  testing, component-library / CSS extraction from a codebase, multi-page
  audits, analysis reports.

All Pro features are **future work**. This task ships the *enforcement
infrastructure* those features call, plus the activation CLI and tests. It does
**not** gate any feature that exists today (they are all single-page → free).

## Architecture (approach A — capability gate package)

New package `internal/license`. Single chokepoint, mirrors the
`resolveProjectScope` discipline already in the daemon.

```
internal/license/
  payload.go   Payload struct (Email, CustomerID, IssuedAt, Expiry, Plan,
               Capabilities []string) + JSON (un)marshal
  key.go       embedded public key (go:embed pubkey.b32) -> *lk.PublicKey;
               package var verifyKey, test override hook
  store.go     XDG state path; Save/Load/Remove the raw license blob
  license.go   Validate(blob) (*Payload, error) — lk.Verify + decode;
               Evaluate(payload, now) Status — state machine + 14d grace
  gate.go      Capability constants; Manager caches loaded license;
               Check(cap) error — the guard Pro code paths call
  issue.go     Mint(privB32, payload) (blob, error) — server/test seam
```

### Capabilities

Named constants (string-valued), e.g. `CapWholeSite`, `CapAnalysisReport`,
`CapComponentExtract`. A license payload lists the capabilities it grants.
`Check(cap)` succeeds only if the license is in a serving state **and** the
payload grants `cap`. Future Pro features add a constant + one `Check` line.

### State machine (`Evaluate`, trust `time.Now()`)

Good-faith compliance only — no clock-tamper hardening.

| Condition | State | Pro allowed? |
|-----------|-------|--------------|
| no blob on disk | `Missing` | no |
| signature/parse fails | `Invalid` | no |
| `now < expiry` | `Valid` | yes |
| `expiry ≤ now < expiry+14d` | `Grace` | yes (warn) |
| `now ≥ expiry+14d` | `Expired` | no |

`Check` returns `nil` for `Valid`/`Grace` (Grace attaches a warning surfaced to
the agent/CLI), and a clear activation-pointing error for
`Missing`/`Invalid`/`Expired`.

### Caching

Daemon and CLI load + verify the blob once into a `Manager` held in memory.
`agnt activate` rewrites the state file; the next process load picks it up.
ECDSA verify stays off all hot paths.

## CLI surface (`cmd/agnt/license.go`)

- `agnt activate <key>` — validate blob, persist to XDG state, print status.
- `agnt license status` — current state, email, expiry, days left, capabilities.
- `agnt license deactivate` — remove the stored blob.
- `agnt license issue --key <privB32> --email … --days … --caps …` — mint a
  license. This is the exact function a Stripe webhook calls server-side; kept
  client-side too for manual issuance and testing.
- `agnt license keygen` — print a fresh keypair (operator/server setup).

## Stripe seam (out of scope, documented)

`issue.Mint(privB32, payload)` is the integration point. A future self-hosted
HTTP service handles the Stripe `checkout.session.completed` webhook, builds a
`Payload` from the customer record, calls `Mint` with the signing private key,
and emails the blob. This task delivers `Mint` and the embedded public key so
that server is a thin wrapper; it does not implement the webhook or HTTP layer.

## Keys

- Public key embedded at `internal/license/pubkey.b32` (committed).
- Signing private key generated once, stored **outside the repo**
  (`~/.config/agnt/license-signing-key.b32`, mode 600). Never committed.
- Tests generate an ephemeral keypair and override `verifyKey` via the test
  hook, so the suite never depends on the embedded production key.

## Testing

- `payload` round-trip marshal/unmarshal.
- `Validate`: good blob, tampered blob (sig fail), wrong-key blob, garbage.
- `Evaluate`: table test across all five states incl. exact grace boundaries.
- `store`: save/load/remove round-trip under a temp XDG dir.
- `gate.Check`: capability granted vs not, per state.
- `issue.Mint` → `Validate` end-to-end with an ephemeral key.

## Dependency note

`hyperboloide/lk@v0.0.0-20251220053519-b291812e3216` (Dec-2025 tip), vendored.
It uses the `ecdsa.ParseUncompressedPublicKey` / `PublicKey.Bytes` APIs, which
are present in the repo's go1.25.11 toolchain — it compiles cleanly. (An earlier
build attempt failed only under a stale go1.22 fallback toolchain.) The embedded
public key and the signing private key were generated with this exact version so
their encodings match.
