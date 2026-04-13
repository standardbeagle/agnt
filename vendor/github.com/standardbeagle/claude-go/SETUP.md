# GitHub Repository Setup Guide

This guide walks through setting up the private GitHub repository for `claude-go`.

## 1. Create Private Repository on GitHub

1. Go to [github.com/standardbeagle](https://github.com/standardbeagle)
2. Click "New repository"
3. Repository name: `claude-go`
4. Description: `Go library for automating Claude CLI with concurrent sessions and streaming`
5. **Set to Private** ✅
6. **Do NOT initialize** with README (we have our own)
7. Click "Create repository"

## 2. Initialize Git and Push

```bash
# Initialize git repository
git init

# Add all files
git add .

# Initial commit
git commit -m "Initial commit: Claude Go library with concurrent session support

- Core library with Client and Session management
- Support for both interactive and single-query modes  
- Comprehensive test suite with file operations
- Examples for basic, concurrent, and interactive usage
- Proper Go module with semantic versioning ready"

# Add remote origin
git remote add origin git@github.com:standardbeagle/claude-go.git

# Push to main branch
git branch -M main
git push -u origin main
```

## 3. Create Initial Release Tag

```bash
# Create and push first version tag
git tag -a v1.0.0 -m "v1.0.0: Initial release

Features:
- Concurrent session management
- Interactive and single-query modes
- Real-time streaming support
- File operations and multi-turn conversations
- Thread-safe operations with proper cleanup
- Comprehensive test suite"

git push origin v1.0.0
```

## 4. Repository Settings

### Access Control (Private Repo)
- Go to Settings → Manage access
- Add team members as needed
- Set appropriate permissions (Read, Write, Admin)

### GitHub Actions (Optional)
Create `.github/workflows/test.yml` for CI:

```yaml
name: Test
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v4
      with:
        go-version: '1.21'
    - run: go test -v ./...
    - run: go build -v ./...
```

### Branch Protection (Optional)
- Go to Settings → Branches
- Add rule for `main` branch
- Require pull request reviews
- Require status checks to pass

## 5. Using the Private Module

### In Go Projects

```bash
# For standardbeagle organization members
go get github.com/standardbeagle/claude-go@v1.0.0

# If using SSH keys
git config --global url."git@github.com:".insteadOf "https://github.com/"

# For development with local changes
go mod edit -replace github.com/standardbeagle/claude-go=../claude-go
```

### Environment Setup for Private Repos

```bash
# Set GOPRIVATE to skip proxy for private repos
export GOPRIVATE=github.com/standardbeagle/*

# Or in go.env
echo "GOPRIVATE=github.com/standardbeagle/*" >> ~/.bashrc
```

## 6. Documentation

### README Badges
- Go Reference: Will work once the repo is public or when pkg.go.dev indexes it
- Go Report Card: Will work after first push
- License badge: Already configured

### Go Module Documentation
The module will be documented at:
- Private: Available to org members via GitHub
- If made public later: https://pkg.go.dev/github.com/standardbeagle/claude-go

## 7. Development Workflow

### For Contributors
```bash
# Clone the repo
git clone git@github.com:standardbeagle/claude-go.git
cd claude-go

# Create feature branch
git checkout -b feature/new-feature

# Make changes, test, commit
go test ./...
git add .
git commit -m "Add new feature"

# Push and create PR
git push origin feature/new-feature
```

### Release Process
```bash
# Update version and create release
git tag -a v1.1.0 -m "v1.1.0: Description of changes"
git push origin v1.1.0

# Create GitHub release from tag with release notes
```

## 8. Next Steps

1. **Create the repository** following steps 1-3
2. **Test the module** by using it in another project
3. **Set up CI/CD** if needed
4. **Document any org-specific requirements**
5. **Share access** with team members

The repository will be available at:
`https://github.com/standardbeagle/claude-go` (private)

And importable as:
`github.com/standardbeagle/claude-go`