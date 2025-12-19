#!/bin/bash
# Release script - updates all version files and creates a git tag
#
# Usage: ./scripts/release.sh 0.6.4
#        ./scripts/release.sh patch   # auto-increment patch version
#        ./scripts/release.sh minor   # auto-increment minor version
#        ./scripts/release.sh --build # Build release after creating tag

set -e

BUILD_RELEASE=false

# Check for --build flag
if [ "$1" = "--build" ]; then
    BUILD_RELEASE=true
    shift
fi

# Get current version from Go source
CURRENT_VERSION=$(grep 'appVersion = ' cmd/agnt/main.go | sed 's/.*"\(.*\)"/\1/')

if [ -z "$1" ]; then
    echo "Current version: $CURRENT_VERSION"
    echo ""
    echo "Usage: $0 <version|patch|minor|major>"
    echo "  $0 0.6.4       # Set specific version"
    echo "  $0 patch       # Increment patch (0.6.3 -> 0.6.4)"
    echo "  $0 minor       # Increment minor (0.6.3 -> 0.7.0)"
    echo "  $0 major       # Increment major (0.6.3 -> 1.0.0)"
    exit 1
fi

# Parse version components
IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

case "$1" in
    patch)
        NEW_VERSION="$MAJOR.$MINOR.$((PATCH + 1))"
        ;;
    minor)
        NEW_VERSION="$MAJOR.$((MINOR + 1)).0"
        ;;
    major)
        NEW_VERSION="$((MAJOR + 1)).0.0"
        ;;
    *)
        NEW_VERSION="$1"
        ;;
esac

echo "Releasing version: $CURRENT_VERSION -> $NEW_VERSION"
echo ""

# Check for uncommitted changes
if ! git diff --quiet || ! git diff --staged --quiet; then
    echo "Error: You have uncommitted changes. Commit or stash them first."
    exit 1
fi

# Update Go version (main.go)
echo "Updating cmd/agnt/main.go..."
sed -i "s/appVersion = \".*\"/appVersion = \"$NEW_VERSION\"/" cmd/agnt/main.go

# Update Go version (daemon.go)
echo "Updating internal/daemon/daemon.go..."
sed -i "s/var Version = \".*\"/var Version = \"$NEW_VERSION\"/" internal/daemon/daemon.go

# Update npm package version
echo "Updating npm/agnt/package.json..."
sed -i "s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/" npm/agnt/package.json

# Update Python package version (both pyproject.toml and __init__.py)
echo "Updating python/agnt/pyproject.toml..."
sed -i "s/^version = \".*\"/version = \"$NEW_VERSION\"/" python/agnt/pyproject.toml
echo "Updating python/agnt/src/agnt/__init__.py..."
sed -i "s/__version__ = \".*\"/__version__ = \"$NEW_VERSION\"/" python/agnt/src/agnt/__init__.py

# Update Claude Code plugin version
echo "Updating plugins/agnt/.claude-plugin/plugin.json..."
sed -i "s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/" plugins/agnt/.claude-plugin/plugin.json

# Update CLAUDE.md
echo "Updating CLAUDE.md..."
sed -i "s/\*\*Version\*\*: .*/\*\*Version\*\*: $NEW_VERSION/" CLAUDE.md

# Update root package.json
echo "Updating package.json..."
sed -i "0,/\"version\": \".*\"/{s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/}" package.json

# Update deprecated npm wrapper (version and dependency)
echo "Updating npm/devtool-mcp/package.json..."
sed -i "0,/\"version\": \".*\"/{s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/}" npm/devtool-mcp/package.json
sed -i "s/\"@standardbeagle\/agnt\": \".*\"/\"@standardbeagle\/agnt\": \"^$NEW_VERSION\"/" npm/devtool-mcp/package.json

# Update deprecated Python wrapper (version and dependency)
echo "Updating python/pyproject.toml..."
sed -i "s/^version = \".*\"/version = \"$NEW_VERSION\"/" python/pyproject.toml
sed -i "s/\"agnt>=.*\"/\"agnt>=$NEW_VERSION\"/" python/pyproject.toml

# Update marketplace metadata (both locations)
echo "Updating .claude-plugin/marketplace.json..."
# Update metadata.version
sed -i '/"metadata": {/,/}/{s/"version": ".*"/"version": "'$NEW_VERSION'"/}' .claude-plugin/marketplace.json
# Update plugins[].version
sed -i '/plugins.*\[/,/\]/{s/"version": ".*"/"version": "'$NEW_VERSION'"/}' .claude-plugin/marketplace.json

# Verify updates
echo ""
echo "Version files updated:"
grep 'appVersion = ' cmd/agnt/main.go
grep 'var Version = ' internal/daemon/daemon.go
grep '"version"' npm/agnt/package.json
grep '^version = ' python/agnt/pyproject.toml
grep '__version__ = ' python/agnt/src/agnt/__init__.py
grep '"version"' plugins/agnt/.claude-plugin/plugin.json
grep '^\*\*Version\*\*:' CLAUDE.md
grep '"version"' package.json | head -1
grep '"version"' npm/devtool-mcp/package.json | head -1
grep '^version = ' python/pyproject.toml
grep '"version"' .claude-plugin/marketplace.json

# Commit and tag
echo ""
echo "Creating commit and tag..."
git add cmd/agnt/main.go internal/daemon/daemon.go npm/agnt/package.json python/agnt/pyproject.toml python/agnt/src/agnt/__init__.py plugins/agnt/.claude-plugin/plugin.json CLAUDE.md package.json npm/devtool-mcp/package.json python/pyproject.toml .claude-plugin/marketplace.json
git commit -m "chore: bump version to $NEW_VERSION

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>"

git tag -a "v$NEW_VERSION" -m "v$NEW_VERSION"

echo ""
echo "Done! To push the release:"
echo "  git push origin main && git push origin v$NEW_VERSION"

# Optionally build the release
if [ "$BUILD_RELEASE" = true ]; then
    echo ""
    echo "Building release binaries..."
    make clean
    make release
    echo ""
    echo "Release binaries built successfully:"
    ls -lh agnt devtool-mcp
fi
