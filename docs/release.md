# Release Process

This document describes the version management and release process for agnt.

## Version Files

The version number is managed across multiple files:

### Core Binaries
- `cmd/agnt/main.go` - appVersion variable
- `internal/daemon/daemon.go` - Version variable
- `internal/daemonclient/upgrade.go` - Version variable

### npm Packages
- `npm/agnt/package.json` - Primary npm package
- `npm/devtool-mcp/package.json` - Deprecated wrapper (version + dependency)
- `package.json` (root) - Root package metadata

### Python Packages
- `python/agnt/pyproject.toml` - Primary Python package version
- `python/agnt/src/agnt/__init__.py` - __version__ variable
- `python/pyproject.toml` - Deprecated wrapper (version + dependency)

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
1. Validate the release: the version is `MAJOR.MINOR.PATCH[-prerelease]` and differs from the current one, tag `v<VERSION>` does not exist yet, there are no uncommitted changes, and HEAD is `main` (`--allow-branch` releases from another branch)
2. Update all version files listed above, aborting and restoring them if any substitution matches nothing
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
grep -h 'var Version = ' internal/daemon/daemon.go internal/daemonclient/upgrade.go
grep -h '"version"' npm/agnt/package.json npm/devtool-mcp/package.json package.json
grep -h '^version = ' python/agnt/pyproject.toml python/pyproject.toml
grep -h '__version__ = ' python/agnt/src/agnt/__init__.py
```

## Publishing the Release

After creating the tag:

```bash
# Push to remote
git push origin main && git push origin v<VERSION>

# GitHub Actions will automatically:
# - Build binaries for all platforms
# - Create GitHub release, with a SHA256SUMS asset covering every binary
# - Publish npm packages
# - Publish Python packages
# - Update marketplace
```

## Release checksums

The release workflow (`.github/workflows/release.yml`) writes `SHA256SUMS` in `sha256sum` format (`<hex>  <asset name>`) over every uploaded binary, after the final artifacts are collected, and uploads it with them. Every downloader verifies against it and fails closed, refusing to install on a missing `SHA256SUMS`, a missing entry, or a digest mismatch:

| Downloader | Where it verifies |
|---|---|
| npm: `@standardbeagle/agnt`, and the root `devtool-mcp` package | `npm/agnt/scripts/install.js`, `scripts/install.js` (`verifyChecksum`, before the atomic rename) |
| PyPI (`agnt`) | `python/agnt/src/agnt/__init__.py` (before the rename into the cache) |
| `install.sh` / `install.ps1`, and `agnt upgrade` for a direct install (it runs them) | the scripts, before the binary is moved into place |
| `agnt ssh` bootstrap, release-download source | `internal/sshclient/bootstrap.go` (`remoteDownloadInstall`: the client fetches `SHA256SUMS`, the remote hashes the download) |

A release published without `SHA256SUMS` (every release before this was introduced) can no longer be installed by these downloaders. There is no override.

## Version Consistency

All version files MUST be kept in sync. The release script ensures this. Never update version numbers manually unless you're certain you've updated all files.

## Claude Code Plugin

The Claude Code plugin and its marketplace manifest were moved to a standalone
marketplace repository. This repository no longer ships `plugins/agnt/` or
`.claude-plugin/marketplace.json`; the release script only updates binary +
package versions.
