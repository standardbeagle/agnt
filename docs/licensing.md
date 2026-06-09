# agnt Pro Licensing

Self-hosted, offline license validation for agnt Pro features. No runtime
phone-home, no monthly platform cost. Built on
[`hyperboloide/lk`](https://github.com/hyperboloide/lk) (ECDSA P-384 signatures).

Code: `internal/license/`. CLI: `cmd/agnt/license.go`.
Design: `docs/superpowers/specs/2026-06-08-self-hosted-licensing-design.md`.

## How it works

A license is an `lk` blob: an ECDSA signature over a JSON payload, base32
encoded. The binary embeds **only the public key** (`internal/license/pubkey.b32`)
and validates fully offline. The signing **private key never ships** — it lives
server-side and signs new licenses (the Stripe-webhook seam).

Payload (`internal/license/payload.go`):

| Field | Meaning |
|-------|---------|
| `email` | Licensee (informational) |
| `customer_id` | Upstream Stripe customer ref |
| `plan` | Tier label (e.g. `team`) |
| `issued_at` | Mint time (UTC) |
| `expiry` | End of paid term (UTC) |
| `capabilities` | Pro capability keys this license grants |

## What is gated

The line is **breadth of operation**, not a fixed tool list:

- **Free** — single-page operations: `responsive_audit`, `api_audit`,
  `loading_audit`, accessibility, snapshot, browser debug, process/proxy mgmt.
- **Pro** — whole-site / multi-page / application-wide: advanced testing,
  component-library / CSS extraction, multi-page audits, analysis reports.

All Pro features are **future work**. The current code ships the enforcement
infrastructure (`license.Manager.Check`) those features hook into; no feature
that exists today is gated.

Capability constants (`internal/license/gate.go`): `whole_site`,
`analysis_report`, `component_extract`, `advanced_testing`.

## Enforcement: state machine

`time.Now()` is trusted (good-faith compliance — no clock-tamper hardening).
Grace period is **14 days** past expiry.

| Condition | State | Pro allowed? |
|-----------|-------|--------------|
| no blob installed | `Missing` | no |
| signature / parse fails | `Invalid` | no |
| `now < expiry` | `Valid` | yes |
| `expiry ≤ now < expiry+14d` | `Grace` | yes (with warning) |
| `now ≥ expiry+14d` | `Expired` | no |

`Manager.Check(cap)` returns `nil` for Valid/Grace when the capability is
granted (Grace also returns a renewal warning), otherwise a `*GateError`
carrying the deciding state for tailored messaging.

## CLI

```bash
agnt activate <key>          # validate + install a license blob (offline)
agnt license status          # show state, email, expiry, days left, caps
agnt license deactivate      # remove the installed license

# server / admin
agnt license keygen          # generate a signing keypair (one-time)
agnt license issue --key <privB32> --email x@y.com --days 365 \
                   --caps whole_site,analysis_report   # mint a blob
```

The license blob is stored per-user under XDG state
(`$XDG_STATE_HOME/agnt/license.lk`, default `~/.local/state/agnt/license.lk`),
written atomically at mode 600.

## Keys

- **Public key**: `internal/license/pubkey.b32` (committed, validates offline).
- **Signing private key**: generated once, stored **outside the repo**
  (`~/.config/agnt/license-signing-key.b32`, mode 600). Never committed.
- The embedded public key and the signing private key are a **matched pair**
  generated with the vendored `lk` version — regenerating one requires
  regenerating both and re-embedding the public half.

## Stripe seam (not yet built)

`license.Mint(privKeyB32, payload)` is the integration point. A future
self-hosted HTTP service handles the Stripe `checkout.session.completed`
webhook, builds a `Payload` from the customer record, calls `Mint` with the
signing key, and emails the blob to the customer. The client half (validation,
activation, enforcement) is complete; the webhook/HTTP layer is the remaining
work.

## Testing

```bash
go test -race ./internal/license/
```

Tests generate an ephemeral keypair via `SetVerifyKeyForTest`, so the suite
never depends on the production key. Coverage: mint→validate round-trip,
tamper / wrong-key / garbage rejection, every grace state incl. boundaries,
store round-trip, and `Manager.Check` per state.
