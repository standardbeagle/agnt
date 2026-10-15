#!/bin/bash
# Release script - updates all version files and creates a git tag
#
# Usage: ./scripts/release.sh 0.6.4
#        ./scripts/release.sh patch   # auto-increment patch version
#        ./scripts/release.sh minor   # auto-increment minor version
#        ./scripts/release.sh --build patch       # Build release after creating tag
#        ./scripts/release.sh --allow-branch patch # Release from a branch other than main
#
# Refuses to run unless: the version is MAJOR.MINOR.PATCH (optionally
# -prerelease), differs from the current one, has no existing v<version> tag,
# the tree has no uncommitted changes, and HEAD is main (--allow-branch
# overrides only the branch check). Every version substitution must change its
# file; one that matches nothing aborts the release and restores the files.

set -e

BUILD_RELEASE=false
ALLOW_BRANCH=false

while [ $# -gt 0 ]; do
    case "$1" in
        --build)
            BUILD_RELEASE=true
            shift
            ;;
        --allow-branch)
            ALLOW_BRANCH=true
            shift
            ;;
        -*)
            echo "Error: unknown flag: $1"
            exit 1
            ;;
        *)
            break
            ;;
    esac
done

# Get current version from Go source
CURRENT_VERSION=$(grep 'appVersion = ' cmd/agnt/main.go | sed 's/.*"\(.*\)"/\1/')

if [ -z "$1" ]; then
    echo "Current version: $CURRENT_VERSION"
    echo ""
    echo "Usage: $0 [--build] [--allow-branch] <version|patch|minor|major>"
    echo "  $0 0.6.4       # Set specific version"
    echo "  $0 patch       # Increment patch (0.6.3 -> 0.6.4)"
    echo "  $0 minor       # Increment minor (0.6.3 -> 0.7.0)"
    echo "  $0 major       # Increment major (0.6.3 -> 1.0.0)"
    exit 1
fi

# MAJOR.MINOR.PATCH with an optional -prerelease (release.yml marks -rc,
# -beta and -alpha tags as prereleases).
SEMVER_RE='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'

case "$1" in
    patch|minor|major)
        if ! [[ "$CURRENT_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo "Error: cannot auto-increment current version '$CURRENT_VERSION'; pass an explicit version."
            exit 1
        fi
        ;;
esac

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

if ! [[ "$NEW_VERSION" =~ $SEMVER_RE ]]; then
    echo "Error: '$NEW_VERSION' is not a version of the form MAJOR.MINOR.PATCH[-prerelease] (no leading 'v')."
    exit 1
fi

if [ "$NEW_VERSION" = "$CURRENT_VERSION" ]; then
    echo "Error: version is already $CURRENT_VERSION."
    exit 1
fi

if git rev-parse -q --verify "refs/tags/v$NEW_VERSION" >/dev/null; then
    echo "Error: tag v$NEW_VERSION already exists."
    exit 1
fi

echo "Releasing version: $CURRENT_VERSION -> $NEW_VERSION"
echo ""

# Check for uncommitted changes
if ! git diff --quiet || ! git diff --staged --quiet; then
    echo "Error: You have uncommitted changes. Commit or stash them first."
    exit 1
fi

CURRENT_BRANCH=$(git symbolic-ref --short -q HEAD || true)
if [ "$CURRENT_BRANCH" != "main" ] && [ "$ALLOW_BRANCH" != true ]; then
    echo "Error: releases are cut from main, but HEAD is ${CURRENT_BRANCH:-detached}."
    echo "Pass --allow-branch to release from it anyway."
    exit 1
fi

VERSION_FILES="cmd/agnt/main.go internal/daemon/daemon.go internal/daemonclient/upgrade.go npm/agnt/package.json python/agnt/pyproject.toml python/agnt/src/agnt/__init__.py package.json npm/devtool-mcp/package.json python/pyproject.toml"

# bump FILE SED_EXPR: apply one version substitution, and abort the release
# (restoring every version file, which the clean-tree check above proved
# unmodified) if it left FILE unchanged or without the new version in it.
bump() {
    local file="$1" expr="$2" before
    before=$(cat "$file")
    sed -i "$expr" "$file"
    if [ "$(cat "$file")" = "$before" ] || ! grep -qF "$NEW_VERSION" "$file"; then
        echo "Error: the version substitution in $file matched nothing:"
        echo "  sed '$expr'"
        echo "Restoring the version files; fix the pattern in $0 and rerun."
        # shellcheck disable=SC2086
        git checkout -- $VERSION_FILES
        exit 1
    fi
}

# Update Go version (main.go)
echo "Updating cmd/agnt/main.go..."
bump cmd/agnt/main.go "s/appVersion = \".*\"/appVersion = \"$NEW_VERSION\"/"

# Update Go version (daemon.go + daemonclient/upgrade.go — kept in sync deliberately)
echo "Updating internal/daemon/daemon.go..."
bump internal/daemon/daemon.go "s/var Version = \".*\"/var Version = \"$NEW_VERSION\"/"
bump internal/daemonclient/upgrade.go "s/var Version = \".*\"/var Version = \"$NEW_VERSION\"/"

# Update npm package version
echo "Updating npm/agnt/package.json..."
bump npm/agnt/package.json "s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/"

# Update Python package version (both pyproject.toml and __init__.py)
echo "Updating python/agnt/pyproject.toml..."
bump python/agnt/pyproject.toml "s/^version = \".*\"/version = \"$NEW_VERSION\"/"
echo "Updating python/agnt/src/agnt/__init__.py..."
bump python/agnt/src/agnt/__init__.py "s/__version__ = \".*\"/__version__ = \"$NEW_VERSION\"/"

# Update root package.json
echo "Updating package.json..."
bump package.json "0,/\"version\": \".*\"/{s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/}"

# Update deprecated npm wrapper (version and dependency)
echo "Updating npm/devtool-mcp/package.json..."
bump npm/devtool-mcp/package.json "0,/\"version\": \".*\"/{s/\"version\": \".*\"/\"version\": \"$NEW_VERSION\"/}"
bump npm/devtool-mcp/package.json "s/\"@standardbeagle\/agnt\": \".*\"/\"@standardbeagle\/agnt\": \"^$NEW_VERSION\"/"

# Update deprecated Python wrapper (version and dependency)
echo "Updating python/pyproject.toml..."
bump python/pyproject.toml "s/^version = \".*\"/version = \"$NEW_VERSION\"/"
bump python/pyproject.toml "s/\"agnt>=.*\"/\"agnt>=$NEW_VERSION\"/"

# Verify updates
echo ""
echo "Version files updated:"
grep 'appVersion = ' cmd/agnt/main.go
grep 'var Version = ' internal/daemon/daemon.go
grep 'var Version = ' internal/daemonclient/upgrade.go
grep '"version"' npm/agnt/package.json
grep '^version = ' python/agnt/pyproject.toml
grep '__version__ = ' python/agnt/src/agnt/__init__.py
grep '"version"' package.json | head -1
grep '"version"' npm/devtool-mcp/package.json | head -1
grep '^version = ' python/pyproject.toml

# Commit and tag
echo ""
echo "Creating commit and tag..."
# shellcheck disable=SC2086
git add $VERSION_FILES
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
