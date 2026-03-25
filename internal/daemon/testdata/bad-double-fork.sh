#!/usr/bin/env bash
# Bad: classic daemon double-fork. Parent -> child -> grandchild.
# Parent and child exit; grandchild orphaned to init/PID 1.
# Usage: bad-double-fork.sh <port> <pidfile>
set -euo pipefail

PORT="${1:?usage: bad-double-fork.sh <port> <pidfile>}"
PIDFILE="${2:?usage: bad-double-fork.sh <port> <pidfile>}"

echo $$ > "$PIDFILE"

# First fork: child
(
    # Second fork: grandchild (in new session)
    setsid bash -c "
        socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo double-fork grandchild' 2>/dev/null
    " &
    # Child exits immediately
) &

echo "bad-double-fork parent=$$ listening on http://localhost:${PORT}"

# Parent exits after brief delay to let forks complete
sleep 1
