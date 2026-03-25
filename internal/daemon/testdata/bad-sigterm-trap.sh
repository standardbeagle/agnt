#!/usr/bin/env bash
# Bad: traps SIGTERM and ignores it entirely (hangs forever on graceful stop).
# Usage: bad-sigterm-trap.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-sigterm-trap.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-sigterm-trap.sh <port> <pidfile>}"

trap '' SIGTERM

echo $$ > "$PIDFILE"

socat TCP-LISTEN:"$PORT",fork,reuseaddr SYSTEM:"echo bad-sigterm-trap on $$" &

echo "bad-sigterm-trap listening on http://localhost:${PORT}"

while true; do sleep 60; done
