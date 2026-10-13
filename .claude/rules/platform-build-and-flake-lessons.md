---
paths:
  - "**/*_windows.go"
  - "**/*_unix.go"
  - "**/*_linux.go"
  - "**/*_darwin.go"
  - "vendor/**"
  - "go.mod"
  - "go.sum"
---

# Platform build tags and the vendored go-cli-server module

## 1. `!windows`-gated symbols referenced from platform-neutral code

A function defined only under `//go:build !windows` and called from untagged code builds on Linux and breaks only under `GOOS=windows go build ./...`. Fixing one often unmasks the next. Run `make cross-compile-check` after every twin-file fix, not just at the end. CI enforces it (`.github/workflows/cross-compile.yml`).

A platform-gated `cmd/agnt` subcommand needs a `<cmd>_windows.go` stub that registers the same command and fails loud (what is unsupported, tracking task, workaround). Never let it fall through to cobra's "unknown command".

## 2. `vendor/` equals the module; patches go upstream

`vendor/` is a plain copy of the versions in `go.mod`. Never edit a file under it.

- A fix to `github.com/standardbeagle/go-cli-server` (or any dependency) is made in that repo, tagged, and arrives here as a `go.mod` bump plus `make vendor`. There is no `replace` directive, so that is the only route that reaches every build.
- Why: a patch that exists only in `vendor/` is dropped by the next `go mod vendor`, and `go install …@version` ignores `vendor/` altogether, so those users never had it.
- `make vendor-check` vendors into a temp directory and fails on any difference. CI runs it (`.github/workflows/test-linux.yml`) and so does the release build.
- History: the tree used to carry local patches marked `UPSTREAM:`. All of them are in `go-cli-server` v0.5.9, and `vendor/` was byte-identical to that tag when this rule was adopted (2026-10-13). The `UPSTREAM:` comments still in the vendored source came from upstream itself; they mark nothing local.
- The module is a recurring defect source (build-tag drift, PTY drain race, `ProcessManager` Start/Shutdown TOCTOU). Known open upstream item: `process/portlookup_unix.go` finds port owners on macOS with `lsof -ti :PORT`, which also matches clients and remote-port matches; `KillProcessByPort` re-runs that lookup, so it can kill a browser connected to the port. agnt's own lookup (`internal/config/portdetect_unix.go`, `lsofListenerArgs`) is listener-only.

## 3. Empty result for the whole deadline is a data-loss race

A timeout with an empty or missing result (not a late one) is not scheduler load. An `echo` cannot take 15s. Suspect an ordering bug (e.g. closing a PTY master before the reader drained it), add a completion barrier, then lower the deadline. See `flake-triage` skill.
