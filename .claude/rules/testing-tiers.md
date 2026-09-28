---
paths:
  - "**/*_test.go"
  - "Makefile"
---

# Test tiers and build tags

| Tag | Target | Contents | Why it is separate |
|---|---|---|---|
| (none) | `make test` | Host-safe default suite | — |
| `procisolation` | `make test-isolated` | `internal/daemon/daemon_orphan_pgid_test.go`, `internal/platform/orphanpgid_unix_test.go` | Real `/proc` and `kill(2)` against dead-leader pgids; natively they could reap unrelated same-uid processes |
| `sshe2e` | `make test-ssh` | Containerized sshd smoke | Needs Docker/Podman (loud skip without). `SSH_E2E_IMAGE` pins the image; a port-22 image also needs `SSH_E2E_USER` |
| `chromee2e` | `make test-chrome-e2e` | Every real-Chrome test in `internal/proxy/*_e2e_test.go`, `*_live_test.go`, and `internal/chromedp/{integration,screenshot_iframe,testutil}_test.go` | Renderer starvation under CPU load is non-deterministic; run only on an unloaded machine |
| — | `make test-js` | vitest + jsdom over the shipped scripts in `internal/proxy/scripts/jstest` | Keeps `make test` independent of node |

- Any new real-Chrome test gets `//go:build chromee2e` and `skipIfNoBrowser`, which skips loudly when Chrome is absent or `SKIP_BROWSER_TESTS` is set. Never let it pass silently.
- `internal/proxy/wrap_e2e_test.go` is HTTP-only and stays in the default suite.
- `DaemonConfig.OrphanScanEnabled` is an internal test-safety knob (zero value is false). Never expose it in `.agnt.kdl`. The user-facing opt-out is `session.orphan-pgid-scan`.
- Tests that use `findAgntBinary(t)` exercise the binary, so they build it fresh from source.
