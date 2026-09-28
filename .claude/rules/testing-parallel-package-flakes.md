---
paths:
  - "**/*_test.go"
  - "internal/testutil/**"
  - "internal/daemon/test_helpers.go"
  - "Makefile"
  - ".githooks/**"
---

# Full-suite gate and pre-commit bypass rules

## Full-repo runs serialize packages

Every full-repo gate (CI, pre-commit, worktrack templates, ad hoc) runs `go test -p 1 ./...` (via `tman`/`./test`). Go's default parallel `./...` causes cross-package port and socket contention (`bind: address already in use`, `explicit listen-port NNNNN is in use (owner: daemon.test …)`) that reads as a flaky daemon/proxy/chromedp test but is a harness artifact. Worktrack tasks attach through the `slice-go-p1` template, not `slice-go`.

A `cmd/agnt`-only red under the full gate, on a task that never touched `cmd/agnt`, is a known harness class (port 5173 contention, stray `cmd/agnt/AGENTS.md`). Delete the stray file; never fold it into a commit. Diagnosis steps: `flake-triage` skill.

## Sanctioned `--no-verify` cases

The tracked pre-commit hook (`.githooks/pre-commit`, installed by `make install-hooks`) blocks a commit whose staged packages fail. There are exactly two sanctioned bypasses. Anything else is gate-evasion.

### Case A — RED-first TDD commit

An intentionally failing test committed before its implementation, to pin a bisect point.

1. The body says it is a deliberate RED commit and why the separate bisect point matters.
2. The paired GREEN commit runs the full hook.

Landing test and fix in one commit (RED output quoted in the body) is equally fine when bisectability does not matter.

### Case B — GREEN commit that flakes only on the `-race` hook

Allowed only when all three hold:

1. The failing test is an already-documented, load-sensitive flake in code the diff does not touch.
2. A non-`-race` gate is green (`-p 1 ./...`), and the test passes under `-race` in isolation.
3. A reviewer independently confirms causal independence (no added goroutine, changed path not exercised by the flaking test), recorded in the commit body and review verdict.

If the same test fails under `-p 1` or in isolation, condition 1 is false and the failure is real.
