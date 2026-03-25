#!/usr/bin/env bash
# Bad: parent forwards SIGTERM to child; child traps SIGTERM and sleeps 30s.
# Exceeds the 5s graceful timeout.
# Usage: bad-sigterm-slow-child.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-sigterm-slow-child.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-sigterm-slow-child.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Spawn child that takes 30s to exit on SIGTERM
bash -c "
    trap 'sleep 30; exit 0' SIGTERM
    socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo slow-child' &
    while true; do sleep 60; done
" &
CHILD_PID=$!

echo "bad-sigterm-slow-child parent=$$ child=$CHILD_PID on http://localhost:${PORT}"

# Forward SIGTERM to child, then wait for it
trap "kill $CHILD_PID 2>/dev/null || true; wait $CHILD_PID 2>/dev/null; exit 0" SIGTERM

wait $CHILD_PID 2>/dev/null || true
