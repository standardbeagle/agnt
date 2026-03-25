#!/usr/bin/env bash
# Fake vitest: binds a port, writes PID file, exits cleanly on SIGTERM.
# Usage: fake-vitest.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: fake-vitest.sh <port> <pidfile>}"
PIDFILE="${2:?usage: fake-vitest.sh <port> <pidfile>}"

cleanup() {
    kill "$SOCAT_PID" 2>/dev/null || true
    rm -f "$PIDFILE"
    exit 0
}
trap cleanup SIGTERM SIGINT

echo $$ > "$PIDFILE"

socat TCP-LISTEN:"$PORT",fork,reuseaddr SYSTEM:"echo vitest on $$" &
SOCAT_PID=$!

echo "vitest listening on http://localhost:${PORT}"

wait $SOCAT_PID 2>/dev/null || true
