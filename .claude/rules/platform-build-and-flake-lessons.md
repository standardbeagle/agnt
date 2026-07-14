---
description: Cross-build platform-drift class + wall-clock-flake misdiagnosis pattern, harvested from the win-crossbuild task pair.
source:
  - task: 01KXE2GHZ3SY73QE6CPE91YXQH
  - task: 01KXF2FZFQEG3ENBHMVHFQC47P
  - commit: 19017128
  - date: 2026-07-13
---

# Platform build-tag drift and flake-misdiagnosis lessons

## 1. `!windows`-gated symbol called unconditionally is a silent cross-build break

A function defined only under `//go:build !windows` but referenced from
platform-neutral code compiles fine on the host and breaks only under
`GOOS=windows go build ./...`. This class bit twice in one task
(`hasReleasedResources` in vendored go-cli-server, then `migrateLegacySocket`
in `internal/daemon`) — fixing the first unmasked the second. Treat one hit
as a signal to keep running the cross-build gate until it's clean, not just
once.

Guard: `make cross-compile-check` (`GOOS=windows GOARCH=amd64 go build ./...`)
already exists in the Makefile/CI (`.github/workflows/cross-compile.yml`) —
keep it required, and re-run it after every twin-file fix in the same task,
not just at the end.

Vendored-file caveat: if `go.mod` has no `replace` directive for a vendored
package, a direct edit under `vendor/` is clobbered by the next
`go mod vendor`. Mark such edits with an `UPSTREAM:` comment and track
upstreaming as a follow-up — don't let the fix silently evaporate.

## 2. "Load-sensitive wall-clock flake" is often a mislabeled data-loss race

`TestCreate_SpawnsAndCapturesOutput`'s own code comment blamed CPU/scheduler
load for its ~1-in-5 failures. The actual failure mode was empty output for
the *entire* deadline, not late output — and an `echo` cannot legitimately
take 15s. That shape (permanently-empty result, full-deadline timeout) rules
out scheduling delay by construction: widening the deadline can never fix a
result that never arrives. The real bug was `waitLoop` closing the PTY master
fd before `readLoop` drained buffered output from a fast-exiting child.

Diagnostic rule: when a test times out with an *empty/missing* result (not a
*late* one), suspect a data-loss race first, per Karpathy principle 5
(suspect yourself/the code before the scheduler). Fix ordering (e.g. a
`readDone` completion barrier), and lower the deadline once the race is
fixed, don't raise it.

This is a direct instance of the pattern epic 01KX5M99X1
("Eradicate load-sensitive wall-clock assertions") is hunting — several of
its "widened timeout, NOT root-caused" entries deserve the same
empty-vs-late test before being accepted as genuine liveness margin.
