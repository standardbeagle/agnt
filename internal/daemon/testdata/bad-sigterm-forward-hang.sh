#!/usr/bin/env bash
# Bad: parent traps SIGTERM, forwards to child via kill; child also traps and ignores.
# Both hang indefinitely.
# Usage: bad-sigterm-forward-hang.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-sigterm-forward-hang.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-sigterm-forward-hang.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Spawn child that ignores SIGTERM
bash -c "
    trap '' SIGTERM
    socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo bad-child' &
    while true; do sleep 60; done
" &
CHILD_PID=$!

echo "bad-sigterm-forward-hang parent=$$ child=$CHILD_PID on http://localhost:${PORT}"

# Parent forwards SIGTERM to child (which ignores it), then keeps waiting.
# Use "sleep 60 || true" to prevent set -e from exiting when sleep is
# interrupted by a signal (sleep returns 128+signum on signal delivery).
trap "kill $CHILD_PID 2>/dev/null || true" SIGTERM

while true; do sleep 60 || true; done
