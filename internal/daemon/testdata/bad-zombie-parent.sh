#!/usr/bin/env bash
# Bad: spawns child that exits immediately, parent never calls wait (creates zombie).
# Usage: bad-zombie-parent.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-zombie-parent.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-zombie-parent.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Spawn child that exits immediately (becomes zombie since parent never waits)
bash -c "exit 0" &

socat TCP-LISTEN:"$PORT",fork,reuseaddr SYSTEM:"echo zombie-parent on $$" &

echo "bad-zombie-parent=$$ listening on http://localhost:${PORT}"

cleanup() {
    rm -f "$PIDFILE"
    exit 0
}
trap cleanup SIGTERM SIGINT

# Busy-loop without calling wait (zombie stays)
while true; do sleep 60; done
