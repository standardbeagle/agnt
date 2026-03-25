#!/usr/bin/env bash
# Bad: child does setsid + nohup + closes fd 0/1/2 to fully detach from parent.
# Usage: bad-fork-detach.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-fork-detach.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-fork-detach.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Spawn fully detached child
setsid nohup bash -c "
    exec 0</dev/null 1>/dev/null 2>/dev/null
    socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo detached-child' 2>/dev/null
" &

echo "bad-fork-detach parent=$$ listening on http://localhost:${PORT}"

# Parent exits immediately; child is orphaned to init
sleep 1
