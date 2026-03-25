#!/usr/bin/env bash
# Fake slow-start: sleeps before binding port (for dependency ordering tests).
# Usage: fake-slow-start.sh <port> <pidfile> [delay_seconds]
set -euo pipefail

PORT="${1:?usage: fake-slow-start.sh <port> <pidfile> [delay]}"
PIDFILE="${2:?usage: fake-slow-start.sh <port> <pidfile> [delay]}"
DELAY="${3:-3}"

cleanup() {
    kill "$SOCAT_PID" 2>/dev/null || true
    rm -f "$PIDFILE"
    exit 0
}
trap cleanup SIGTERM SIGINT

echo $$ > "$PIDFILE"
echo "slow-start: waiting ${DELAY}s before binding port ${PORT}"
sleep "$DELAY"

socat TCP-LISTEN:"$PORT",fork,reuseaddr SYSTEM:"echo slow-start on $$" &
SOCAT_PID=$!

echo "slow-start listening on http://localhost:${PORT}"

wait $SOCAT_PID 2>/dev/null || true
