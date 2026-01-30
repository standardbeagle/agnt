#!/bin/bash
#
# Run the full integration test suite for agnt browser automation.
#
# Usage:
#   ./testdata/scripts/run-integration.sh           # Run all integration tests
#   ./testdata/scripts/run-integration.sh browser   # Run only browser tests
#   ./testdata/scripts/run-integration.sh webapp    # Start test webapp only
#   ./testdata/scripts/run-integration.sh agent     # Start mock agent only
#
# Environment variables:
#   AGNT_TEST_CHROME_PATH   - Path to Chrome binary (auto-detected if not set)
#   AGNT_TEST_HEADLESS      - Run browser tests headless (default: true)
#   AGNT_TEST_WEBAPP_PORT   - Test webapp port (default: 18080)
#   AGNT_TEST_TIMEOUT       - Test timeout in seconds (default: 60)
#   AGNT_TEST_VERBOSE       - Enable verbose output (default: false)
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
WEBAPP_PORT="${AGNT_TEST_WEBAPP_PORT:-18080}"
TIMEOUT="${AGNT_TEST_TIMEOUT:-60}"
VERBOSE="${AGNT_TEST_VERBOSE:-false}"

# PIDs to clean up
WEBAPP_PID=""
AGENT_PID=""

cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    if [ -n "$WEBAPP_PID" ] && kill -0 "$WEBAPP_PID" 2>/dev/null; then
        echo "Stopping test webapp (PID $WEBAPP_PID)"
        kill "$WEBAPP_PID" 2>/dev/null || true
    fi

    if [ -n "$AGENT_PID" ] && kill -0 "$AGENT_PID" 2>/dev/null; then
        echo "Stopping mock agent (PID $AGENT_PID)"
        kill "$AGENT_PID" 2>/dev/null || true
    fi
}

trap cleanup EXIT

log() {
    echo -e "${BLUE}[$(date '+%H:%M:%S')]${NC} $1"
}

error() {
    echo -e "${RED}ERROR:${NC} $1" >&2
}

success() {
    echo -e "${GREEN}SUCCESS:${NC} $1"
}

# Check if Chrome is available
check_chrome() {
    if [ -n "$AGNT_TEST_CHROME_PATH" ]; then
        if [ -x "$AGNT_TEST_CHROME_PATH" ]; then
            echo "$AGNT_TEST_CHROME_PATH"
            return 0
        fi
    fi

    # Try common paths
    for path in \
        /usr/bin/google-chrome \
        /usr/bin/google-chrome-stable \
        /usr/bin/chromium \
        /usr/bin/chromium-browser \
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"; do
        if [ -x "$path" ]; then
            echo "$path"
            return 0
        fi
    done

    return 1
}

# Build test binaries
build_test_binaries() {
    log "Building test binaries..."
    cd "$PROJECT_ROOT"

    # Build test webapp
    go build -o testdata/webapps/server/webapp ./testdata/webapps/server/

    # Build mock agent
    go build -o testdata/mockagent/mockagent ./testdata/mockagent/

    success "Test binaries built"
}

# Start test webapp server
start_webapp() {
    log "Starting test webapp on port $WEBAPP_PORT..."

    cd "$PROJECT_ROOT"
    ./testdata/webapps/server/webapp -port "$WEBAPP_PORT" &
    WEBAPP_PID=$!

    # Wait for server to be ready
    for i in $(seq 1 30); do
        if curl -s "http://localhost:$WEBAPP_PORT/api/health" > /dev/null 2>&1; then
            success "Test webapp started (PID $WEBAPP_PID)"
            return 0
        fi
        sleep 0.1
    done

    error "Test webapp failed to start"
    return 1
}

# Start mock agent
start_agent() {
    log "Starting mock agent..."

    cd "$PROJECT_ROOT"
    ./testdata/mockagent/mockagent &
    AGENT_PID=$!

    sleep 0.5

    if kill -0 "$AGENT_PID" 2>/dev/null; then
        success "Mock agent started (PID $AGENT_PID)"
        return 0
    else
        error "Mock agent failed to start"
        return 1
    fi
}

# Run unit tests
run_unit_tests() {
    log "Running unit tests..."
    cd "$PROJECT_ROOT"

    if [ "$VERBOSE" = "true" ]; then
        go test -v ./...
    else
        go test ./...
    fi

    success "Unit tests passed"
}

# Run integration tests
run_integration_tests() {
    log "Running integration tests..."
    cd "$PROJECT_ROOT"

    export AGNT_TEST_WEBAPP_PORT="$WEBAPP_PORT"

    if [ "$VERBOSE" = "true" ]; then
        go test -v -tags=integration -timeout "${TIMEOUT}s" ./...
    else
        go test -tags=integration -timeout "${TIMEOUT}s" ./...
    fi

    success "Integration tests passed"
}

# Run browser tests
run_browser_tests() {
    CHROME_PATH=$(check_chrome) || {
        echo -e "${YELLOW}WARNING:${NC} Chrome not found, skipping browser tests"
        return 0
    }

    log "Running browser tests (Chrome: $CHROME_PATH)..."
    cd "$PROJECT_ROOT"

    export AGNT_TEST_CHROME_PATH="$CHROME_PATH"
    export AGNT_TEST_WEBAPP_PORT="$WEBAPP_PORT"

    if [ "$VERBOSE" = "true" ]; then
        go test -v -tags=integration -timeout "${TIMEOUT}s" ./internal/browser/...
    else
        go test -tags=integration -timeout "${TIMEOUT}s" ./internal/browser/...
    fi

    success "Browser tests passed"
}

# Main
main() {
    cd "$PROJECT_ROOT"

    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}  agnt Integration Test Suite${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo

    case "${1:-all}" in
        webapp)
            build_test_binaries
            start_webapp
            echo -e "\n${GREEN}Test webapp running at http://localhost:$WEBAPP_PORT${NC}"
            echo "Press Ctrl+C to stop"
            wait "$WEBAPP_PID"
            ;;
        agent)
            build_test_binaries
            start_agent
            echo -e "\n${GREEN}Mock agent running${NC}"
            echo "Press Ctrl+C to stop"
            wait "$AGENT_PID"
            ;;
        browser)
            build_test_binaries
            start_webapp
            run_browser_tests
            ;;
        unit)
            run_unit_tests
            ;;
        integration)
            build_test_binaries
            start_webapp
            run_integration_tests
            ;;
        all)
            build_test_binaries
            run_unit_tests
            start_webapp
            run_integration_tests
            ;;
        *)
            echo "Usage: $0 [all|unit|integration|browser|webapp|agent]"
            exit 1
            ;;
    esac

    echo
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  All tests completed successfully!${NC}"
    echo -e "${GREEN}========================================${NC}"
}

main "$@"
