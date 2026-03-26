#!/usr/bin/env bash
# Fake server that fails with EADDRINUSE if port is already taken.
# On retry (port is free), it succeeds normally.
# Usage: fake-eaddrinuse.sh <port> <pidfile>
set -uo pipefail

PORT="${1:?usage: fake-eaddrinuse.sh <port> <pidfile>}"
PIDFILE="${2:?usage: fake-eaddrinuse.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# Try to bind the port WITHOUT reuseaddr so we get a real bind failure
socat TCP-LISTEN:"$PORT",fork SYSTEM:"echo eaddrinuse-test on $$" &
SOCAT_PID=$!

# Give socat a moment to bind or fail
sleep 0.3

# Check if socat is still alive (successful bind)
if kill -0 "$SOCAT_PID" 2>/dev/null; then
    echo "fake-eaddrinuse listening on http://localhost:${PORT}"
    cleanup() {
        kill "$SOCAT_PID" 2>/dev/null || true
        rm -f "$PIDFILE"
        exit 0
    }
    trap cleanup SIGTERM SIGINT
    wait "$SOCAT_PID" 2>/dev/null || true
else
    # socat failed - emit EADDRINUSE pattern and exit
    echo "Error: listen EADDRINUSE: address already in use :${PORT}" >&2
    rm -f "$PIDFILE"
    exit 1
fi
