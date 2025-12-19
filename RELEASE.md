# Release Process

This document describes the version management and release process for agnt.

## Version Files

The version number is managed across multiple files:

### Core Binaries
- `cmd/agnt/main.go` - appVersion variable
- `internal/daemon/daemon.go` - Version variable

### npm Packages
- `npm/agnt/package.json` - Primary npm package
- `npm/devtool-mcp/package.json` - Deprecated wrapper (version + dependency)
- `package.json` (root) - Root package metadata

### Python Packages
- `python/agnt/pyproject.toml` - Primary Python package version
- `python/agnt/src/agnt/__init__.py` - __version__ variable
- `python/pyproject.toml` - Deprecated wrapper (version + dependency)

### Plugin Metadata
- `plugins/agnt/.claude-plugin/plugin.json` - Claude Code plugin version
- `.claude-plugin/marketplace.json` - Marketplace metadata (2 locations)

### Documentation
- `CLAUDE.md` - Project overview version

## Automated Version Management

Use the `scripts/release.sh` script to update all version files automatically:

```bash
# Increment patch version (0.7.7 → 0.7.8)
./scripts/release.sh patch

# Increment minor version (0.7.7 → 0.8.0)
./scripts/release.sh minor

# Increment major version (0.7.7 → 1.0.0)
./scripts/release.sh major

# Set specific version
./scripts/release.sh 0.8.0

# Build release binaries after tagging
./scripts/release.sh --build patch
```

The script will:
1. Validate no uncommitted changes exist
2. Update all version files listed above
3. Create a git commit with all changes
4. Create a git tag `v<VERSION>`
5. Display push instructions
6. Optionally build release binaries

## Manual Version Update

If you need to update versions manually, ensure you update ALL files listed above to maintain consistency.

## Verification

After running the release script, verify all versions are consistent:

```bash
# Check binary version
agnt --version

# Check all version files
grep -h 'appVersion = ' cmd/agnt/main.go
grep -h 'var Version = ' internal/daemon/daemon.go
grep -h '"version"' npm/agnt/package.json npm/devtool-mcp/package.json package.json
grep -h '^version = ' python/agnt/pyproject.toml python/pyproject.toml
grep -h '__version__ = ' python/agnt/src/agnt/__init__.py
grep -h '"version"' plugins/agnt/.claude-plugin/plugin.json
grep -h '"version"' .claude-plugin/marketplace.json
grep '^\*\*Version\*\*:' CLAUDE.md
```

## Publishing the Release

After creating the tag:

```bash
# Push to remote
git push origin main && git push origin v<VERSION>

# GitHub Actions will automatically:
# - Build binaries for all platforms
# - Create GitHub release
# - Publish npm packages
# - Publish Python packages
# - Update marketplace
```

## Version Consistency

All version files MUST be kept in sync. The release script ensures this. Never update version numbers manually unless you're certain you've updated all files.

## Marketplace Updates

The `.claude-plugin/marketplace.json` file contains two version fields:
- `metadata.version` - Marketplace version
- `plugins[0].version` - Plugin version

Both must match the binary version.
