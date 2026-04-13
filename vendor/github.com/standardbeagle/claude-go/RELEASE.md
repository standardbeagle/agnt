# Release Preparation Checklist

Use this checklist before creating a new release of claude-go.

## Pre-Release Checklist

### Code Quality
- [ ] All tests pass: `make test`
- [ ] Examples build: `make examples`  
- [ ] Code is formatted: `go fmt ./...`
- [ ] No linting issues: `go vet ./...`
- [ ] Dependencies are tidy: `go mod tidy`
- [ ] Security scan clean: `gosec ./...` (if available)

### Documentation
- [ ] README.md is up to date
- [ ] Examples are working and documented
- [ ] CHANGELOG.md updated with changes
- [ ] API documentation (doc.go) is current
- [ ] All public functions have proper Go doc comments

### Testing
- [ ] Unit tests pass: `make test-short`
- [ ] Integration tests pass (if Claude CLI available): `make test-integration`
- [ ] Examples can be built and run
- [ ] Concurrent operations tested
- [ ] File operations tested
- [ ] Multi-turn conversations tested

### Version Management
- [ ] Version number follows semantic versioning (vX.Y.Z)
- [ ] CHANGELOG.md includes new version entry
- [ ] Breaking changes are clearly documented
- [ ] Migration guide provided (if needed)

## Release Process

### 1. Final Preparation
```bash
# Clean build
make clean
make build

# Run full test suite
make test-all

# Verify examples work
make examples
```

### 2. Create Release
```bash
# Create version tag
git tag -a v1.x.x -m "v1.x.x: Brief description of changes

Detailed release notes:
- Feature 1
- Feature 2  
- Bug fix 1"

# Push tag
git push origin v1.x.x
```

### 3. GitHub Release
- [ ] Go to GitHub → Releases → "Create a new release"
- [ ] Select the version tag
- [ ] Add release title: "v1.x.x - Brief Description"
- [ ] Add detailed release notes (copy from tag message)
- [ ] Mark as pre-release if appropriate
- [ ] Publish release

### 4. Post-Release
- [ ] Verify release appears on GitHub
- [ ] Test installation: `go get github.com/standardbeagle/claude-go@v1.x.x`
- [ ] Update any dependent projects
- [ ] Announce to team/users if appropriate

## Release Types

### Patch Release (v1.0.X)
- Bug fixes
- Documentation updates
- Minor improvements
- No breaking changes

### Minor Release (v1.X.0)
- New features
- Enhancements
- Backwards compatible changes
- New configuration options

### Major Release (vX.0.0)
- Breaking changes
- Major architectural changes
- API changes requiring user code updates
- Requires migration guide

## Common Release Scenarios

### Bug Fix Release
```bash
# After fixing bugs
git add .
git commit -m "Fix session cleanup race condition"
git tag -a v1.0.1 -m "v1.0.1: Fix session cleanup race condition"
git push origin v1.0.1
```

### Feature Release  
```bash
# After adding new features
git add .
git commit -m "Add support for custom Claude models"  
git tag -a v1.1.0 -m "v1.1.0: Add custom model support"
git push origin v1.1.0
```

### Breaking Change Release
```bash
# After breaking changes (requires CHANGELOG and migration docs)
git add .
git commit -m "BREAKING: Change Options struct field names for clarity"
git tag -a v2.0.0 -m "v2.0.0: Breaking changes for improved API"
git push origin v2.0.0
```

## Emergency Hotfix Process

For critical security or stability issues:

1. Create hotfix branch from latest release tag
2. Apply minimal fix
3. Test thoroughly
4. Create patch release immediately
5. Merge back to main branch

```bash
git checkout v1.2.3
git checkout -b hotfix/security-fix
# Apply fix
git commit -m "Security: Fix authentication bypass"
git tag -a v1.2.4 -m "v1.2.4: Critical security fix"
git push origin v1.2.4
git checkout main
git merge hotfix/security-fix
```

## Rollback Process

If a release has critical issues:

1. Document the issue
2. Create hotfix if possible
3. If not fixable quickly, advise users to downgrade:
   ```bash
   go get github.com/standardbeagle/claude-go@v1.x.y
   ```
4. Create new release with fix

## Automation

Consider automating parts of this process with:
- GitHub Actions for testing on tag push
- Automated CHANGELOG generation
- Release note generation from commit messages
- Automated version bumping tools