#!/usr/bin/env bash
# Bad: spawns 10 grandchildren via background sleep before main loop.
# Usage: bad-fork-bomb-lite.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-fork-bomb-lite.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-fork-bomb-lite.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Rapidly spawn 10 grandchildren
for i in $(seq 1 10); do
    sleep 3600 &
done

socat TCP-LISTEN:"$PORT",fork,reuseaddr SYSTEM:"echo fork-bomb-lite on $$" &
SOCAT_PID=$!

echo "bad-fork-bomb-lite parent=$$ listening on http://localhost:${PORT}"

cleanup() {
    rm -f "$PIDFILE"
    exit 0
}
trap cleanup SIGTERM SIGINT

wait $SOCAT_PID 2>/dev/null || true
wait
