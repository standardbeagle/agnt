#!/bin/bash
# Environment initialization for Daemon Architecture Migration
# Generated: 2025-12-06

set -e  # Exit on error

echo "Initializing environment for daemon architecture work..."

# 1. Verify Go installation
if ! command -v go &> /dev/null; then
    echo "❌ Go not found. Please install Go 1.24.2 or later."
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "✅ Go version: $GO_VERSION"

# 2. Verify project structure
if [ ! -f "go.mod" ]; then
    echo "❌ Not in project root (go.mod not found)"
    exit 1
fi

MODULE=$(head -1 go.mod | awk '{print $2}')
echo "✅ Module: $MODULE"

# 3. Download dependencies
echo "Downloading dependencies..."
go mod download

# 4. Verify build works
echo "Verifying build..."
if go build ./...; then
    echo "✅ Build successful"
else
    echo "❌ Build failed"
    exit 1
fi

# 5. Run existing tests
echo "Running existing tests..."
if go test ./... -short; then
    echo "✅ Existing tests pass"
else
    echo "⚠️ Some tests failed - review before proceeding"
fi

# 6. Create new package directories
echo "Creating package structure..."
mkdir -p internal/protocol
mkdir -p internal/daemon
mkdir -p internal/client

# 7. Check for stale daemon socket (cleanup if exists)
SOCKET_PATH="/tmp/devtool-mcp-$(id -u).sock"
if [ -S "$SOCKET_PATH" ]; then
    echo "⚠️ Found existing socket at $SOCKET_PATH"
    echo "   If no daemon is running, remove with: rm $SOCKET_PATH"
fi

# 8. Display task summary
echo ""
echo "=========================================="
echo "Environment ready for daemon architecture"
echo "=========================================="
echo ""
echo "Tasks to complete (see tasks.json):"
echo "  Phase 1: Core infrastructure (3 tasks)"
echo "  Phase 2: Command handlers (1 task)"
echo "  Phase 3: MCP client shim (3 tasks)"
echo "  Phase 4: Daemon tool + main.go (2 tasks)"
echo "  Phase 5: Windows support (1 task)"
echo "  Phase 6: Testing & docs (4 tasks)"
echo ""
echo "Start with: task-001 (protocol package)"
echo "Verify with: go test -v ./internal/protocol/..."
echo ""
echo "Key references:"
echo "  - .claude/rules/daemon-architecture.md (architecture)"
echo "  - tasks.json (task definitions)"
echo "  - progress.txt (session log)"
