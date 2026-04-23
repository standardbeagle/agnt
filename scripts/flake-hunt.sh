#!/usr/bin/env bash
# tight-loop isolation for hunting flaky tests
# Usage: scripts/flake-hunt.sh <pkg> <test-regex> [count]
# Example: scripts/flake-hunt.sh ./internal/daemon TestScriptLifecycle 20
set -euo pipefail
pkg="${1:?pkg required}"
test="${2:?test regex required}"
count="${3:-200}"
exec go test -race -count="$count" -run="$test" "$pkg"
