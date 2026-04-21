#!/usr/bin/env bash
# Patch vendored dependencies after go mod vendor.
# Add patches here for any vendored deps that need fixes not yet upstream.
set -e

FILE="vendor/github.com/standardbeagle/go-cli-server/process/lifecycle.go"
if grep -q 'snapshotDescendantsOf' "$FILE"; then
  echo "go-cli-server already patched"
else
  # lifecycle.go calls findAllDescendants unconditionally, but it's only
  # defined in lifecycle_unix.go (!windows). Replace with platform-safe wrapper.
  sed -i 's/descendants := findAllDescendants(pid)/descendants := snapshotDescendantsOf(pid)/' "$FILE"

  # Add snapshotDescendantsOf wrapper to unix file
  UFILE="vendor/github.com/standardbeagle/go-cli-server/process/lifecycle_unix.go"
  sed -i '/^func findAllDescendants(pid int) \[\]int {/i\
// snapshotDescendantsOf delegates to findAllDescendants (unix-only impl).\
func snapshotDescendantsOf(pid int) []int {\
	return findAllDescendants(pid)\
}\
' "$UFILE"

  # Add stub to windows file
  WFILE="vendor/github.com/standardbeagle/go-cli-server/process/lifecycle_windows.go"
  if ! grep -q 'snapshotDescendantsOf' "$WFILE"; then
    sed -i '/^\/\/ cleanupProcessTree/i\
// snapshotDescendantsOf returns nil on Windows — Job Objects handle cleanup.\
func snapshotDescendantsOf(pid int) []int { return nil }\
' "$WFILE"
  fi

  echo "Patched go-cli-server for Windows cross-compile"
fi
