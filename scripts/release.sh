#!/bin/bash
# Release script - updates all version files and creates a git tag
#
# Usage: ./scripts/release.sh 0.6.4
#        ./scripts/release.sh patch   # auto-increment patch version
#        ./scripts/release.sh minor   # auto-increment minor version

set -e

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

# Verify updates
echo ""
echo "Version files updated:"
grep 'appVersion = ' cmd/agnt/main.go
grep 'var Version = ' internal/daemon/daemon.go
grep '"version"' npm/agnt/package.json
grep '^version = ' python/agnt/pyproject.toml
grep '__version__ = ' python/agnt/src/agnt/__init__.py

# Commit and tag
echo ""
echo "Creating commit and tag..."
git add cmd/agnt/main.go internal/daemon/daemon.go npm/agnt/package.json python/agnt/pyproject.toml python/agnt/src/agnt/__init__.py
git commit -m "chore: bump version to $NEW_VERSION

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>"

git tag -a "v$NEW_VERSION" -m "v$NEW_VERSION"

echo ""
echo "Done! To push the release:"
echo "  git push origin main && git push origin v$NEW_VERSION"
