#!/bin/bash
#
# Helper script to manage agnt proxies for e2e testing
#
# Usage:
#   ./proxy-helper.sh start <id> <target_url> <port>
#   ./proxy-helper.sh stop <id>
#   ./proxy-helper.sh status <id>
#

set -e

AGNT_BIN="${AGNT_BIN:-../agnt}"
SOCKET_PATH="${SOCKET_PATH:-/tmp/devtool-mcp-$(whoami).sock}"

# Function to send command to daemon via socket
send_to_daemon() {
    local cmd="$1"
    if [[ -S "$SOCKET_PATH" ]]; then
        echo "$cmd" | nc -U "$SOCKET_PATH" -q 1 2>/dev/null || true
    else
        echo "Error: Daemon socket not found at $SOCKET_PATH" >&2
        return 1
    fi
}

# Ensure daemon is running
ensure_daemon() {
    "$AGNT_BIN" daemon start 2>/dev/null || true
    sleep 1
}

case "$1" in
    start)
        if [[ $# -lt 4 ]]; then
            echo "Usage: $0 start <id> <target_url> <port>" >&2
            exit 1
        fi
        ensure_daemon
        ID="$2"
        TARGET="$3"
        PORT="$4"

        # Send PROXY START command
        response=$(send_to_daemon "PROXY START $ID $TARGET $PORT;;")
        echo "$response"

        # Wait for proxy to be ready
        for i in {1..10}; do
            if curl -s -o /dev/null "http://localhost:$PORT/"; then
                echo "Proxy ready on port $PORT"
                exit 0
            fi
            sleep 0.5
        done
        echo "Proxy started but might not be fully ready"
        ;;

    stop)
        if [[ $# -lt 2 ]]; then
            echo "Usage: $0 stop <id>" >&2
            exit 1
        fi
        ID="$2"
        response=$(send_to_daemon "PROXY STOP $ID;;")
        echo "$response"
        ;;

    status)
        if [[ $# -lt 2 ]]; then
            echo "Usage: $0 status <id>" >&2
            exit 1
        fi
        ID="$2"
        response=$(send_to_daemon "PROXY STATUS $ID;;")
        echo "$response"
        ;;

    list)
        response=$(send_to_daemon "PROXY LIST;;")
        echo "$response"
        ;;

    *)
        echo "Usage: $0 {start|stop|status|list} [args...]" >&2
        exit 1
        ;;
esac
