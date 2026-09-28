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

# Platform build tags and the vendored go-cli-server fork

## 1. `!windows`-gated symbols referenced from platform-neutral code

A function defined only under `//go:build !windows` and called from untagged code builds on Linux and breaks only under `GOOS=windows go build ./...`. Fixing one often unmasks the next. Run `make cross-compile-check` after every twin-file fix, not just at the end. CI enforces it (`.github/workflows/cross-compile.yml`).

A platform-gated `cmd/agnt` subcommand needs a `<cmd>_windows.go` stub that registers the same command and fails loud (what is unsupported, tracking task, workaround). Never let it fall through to cobra's "unknown command".

## 2. Vendored fork patches evaporate on `go mod vendor`

`github.com/standardbeagle/go-cli-server` is vendored with local patches and no `replace` directive. `go mod vendor` silently reverts them.

- Mark every vendored edit with an `UPSTREAM:` comment and track upstreaming.
- Any task that runs `go mod vendor`: before committing, grep every `UPSTREAM:`-marked file and confirm each sentinel survived. Keep only the new package plus the `go.mod`/`vendor/modules.txt` lines; `git checkout` the rest of `vendor/`.
- The fork is a recurring defect source (build-tag drift, PTY drain race, `ProcessManager` Start/Shutdown TOCTOU, reverted patch). Prefer fixing upstream over re-patching the vendor copy.

## 3. Empty result for the whole deadline is a data-loss race

A timeout with an empty or missing result (not a late one) is not scheduler load. An `echo` cannot take 15s. Suspect an ordering bug (e.g. closing a PTY master before the reader drained it), add a completion barrier, then lower the deadline. See `flake-triage` skill.
