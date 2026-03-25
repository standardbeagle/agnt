#!/usr/bin/env bash
# Fake dotnet watch: spawns a child in a new session (mimics dotnet process tree).
# Parent forwards SIGTERM to child group. Child binds port.
# Usage: fake-dotnet-watch.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: fake-dotnet-watch.sh <port> <pidfile>}"
PIDFILE="${2:?usage: fake-dotnet-watch.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Spawn the actual server as a child in a new session
setsid bash -c "
    socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo dotnet-watch child'
" &
CHILD_PID=$!

echo "dotnet-watch parent=$$ child=$CHILD_PID listening on http://localhost:${PORT}"

cleanup() {
    kill -- -$CHILD_PID 2>/dev/null || kill $CHILD_PID 2>/dev/null || true
    rm -f "$PIDFILE"
    exit 0
}
trap cleanup SIGTERM SIGINT

wait $CHILD_PID 2>/dev/null || true
