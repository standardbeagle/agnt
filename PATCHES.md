# Vendor Patches

This file tracks modifications applied to vendored dependencies under
`vendor/`. Each entry describes the patch, the reason, the task it was
created under, and how to re-apply the patch after `go mod vendor` (which
will silently re-clobber the vendor tree).

When adding a new patch:
1. Add a `// PATCH: agnt/<task-id>` comment block at the top of every
   modified file describing what was changed and how to restore it.
2. Add an entry to this file with the same information.
3. Ideally, open an upstream issue/PR so the patch can be dropped later.

## Active Patches

### `github.com/standardbeagle/go-cli-server` v0.3.8 — Windows build fix

**Task:** agnt/7YAvGz5W7l5u
**File:** `vendor/github.com/standardbeagle/go-cli-server/process/lifecycle_windows.go`

**Problem:** `process/lifecycle.go:snapshotDescendants` (not build-tagged)
calls `findAllDescendants(pid)`, but `findAllDescendants` is only defined
in `lifecycle_unix.go` (`//go:build !windows`). This breaks
`GOOS=windows GOARCH=amd64 go build ./...`.

**Fix:** Added a Windows stub `findAllDescendants(pid int) []int` that
returns `nil`. This is semantically correct on Windows: process-tree
cleanup goes through Job Objects (`TerminateJobObject`), which kill the
entire tree atomically, so the snapshot path has nothing useful to do on
Windows.

**Re-apply after `go mod vendor`:**
1. Open `vendor/github.com/standardbeagle/go-cli-server/process/lifecycle_windows.go`.
2. Add the `// PATCH: agnt/7YAvGz5W7l5u` comment block after the build tag.
3. Add the `findAllDescendants(pid int) []int { return nil }` stub.
4. Re-run the verification block below.

**Drop condition:** Remove this patch once go-cli-server ships a version
where either (a) `findAllDescendants` is defined for Windows or (b) the
call site is build-tagged to Unix only.

**Verification:**
```sh
GOOS=windows GOARCH=amd64 go build ./vendor/github.com/standardbeagle/go-cli-server/...
GOOS=windows GOARCH=amd64 go vet  ./vendor/github.com/standardbeagle/go-cli-server/...
GOOS=windows GOARCH=amd64 go build ./...
go test ./...
```
