# build-test-binaries.ps1
# Build old and new agnt binaries for upgrade testing

param(
    [string]$OldTag = "v0.6.4",
    [string]$OutputDir = "bin"
)

$ErrorActionPreference = "Stop"

function Write-Info($message) {
    Write-Host "[INFO] $message" -ForegroundColor Yellow
}

function Write-Success($message) {
    Write-Host "[OK] $message" -ForegroundColor Green
}

function Write-Failure($message) {
    Write-Host "[FAIL] $message" -ForegroundColor Red
}

# Check if we're in a git repository
if (-not (Test-Path ".git")) {
    Write-Failure "Not in a git repository. Please run this script from the project root."
    exit 1
}

# Create output directory
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir | Out-Null
    Write-Info "Created output directory: $OutputDir"
}

# Save current state
Write-Info "Saving current git state..."
$currentBranch = git rev-parse --abbrev-ref HEAD
$hasChanges = (git status --porcelain) -ne $null

if ($hasChanges) {
    Write-Info "Stashing uncommitted changes..."
    git stash push -m "build-test-binaries.ps1 auto-stash"
}

try {
    # Build old version
    Write-Info "Checking out old version: $OldTag"

    # Fetch tags to ensure we have the old tag
    git fetch --tags 2>$null

    # Check if tag exists
    $tagExists = git tag -l $OldTag
    if (-not $tagExists) {
        Write-Failure "Tag $OldTag not found"
        Write-Info "Available tags:"
        git tag -l | Write-Host
        throw "Old tag not found"
    }

    # Checkout old tag
    git checkout $OldTag 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to checkout $OldTag"
    }

    Write-Info "Building old version ($OldTag)..."
    go build -o "$OutputDir/agnt-old.exe" ./cmd/agnt/

    if ($LASTEXITCODE -ne 0) {
        throw "Failed to build old version"
    }

    # Get old version
    $oldVersion = & "$OutputDir/agnt-old.exe" --version 2>&1
    Write-Success "Old version built: $oldVersion"
    Write-Success "Binary: $OutputDir/agnt-old.exe"

    # Return to original branch
    Write-Info "Returning to $currentBranch..."
    git checkout $currentBranch 2>$null

    if ($LASTEXITCODE -ne 0) {
        throw "Failed to checkout $currentBranch"
    }

    # Build new version
    Write-Info "Building new version (current)..."
    go build -o "$OutputDir/agnt-new.exe" ./cmd/agnt/

    if ($LASTEXITCODE -ne 0) {
        throw "Failed to build new version"
    }

    # Get new version
    $newVersion = & "$OutputDir/agnt-new.exe" --version 2>&1
    Write-Success "New version built: $newVersion"
    Write-Success "Binary: $OutputDir/agnt-new.exe"

    # Also build agnt.exe for convenience
    Copy-Item "$OutputDir/agnt-new.exe" "$OutputDir/agnt.exe" -Force
    Write-Success "Copied to: $OutputDir/agnt.exe"

    # Summary
    Write-Host "`n========================================" -ForegroundColor Cyan
    Write-Host "       Build Complete" -ForegroundColor Cyan
    Write-Host "========================================`n" -ForegroundColor Cyan

    Write-Host "Old binary: " -NoNewline
    Write-Host "$OutputDir/agnt-old.exe" -ForegroundColor Green
    Write-Host "  Version: $oldVersion" -ForegroundColor Gray

    Write-Host "`nNew binary: " -NoNewline
    Write-Host "$OutputDir/agnt-new.exe" -ForegroundColor Green
    Write-Host "  Version: $newVersion" -ForegroundColor Gray

    Write-Host "`nCurrent binary: " -NoNewline
    Write-Host "$OutputDir/agnt.exe" -ForegroundColor Green
    Write-Host "  Version: $newVersion" -ForegroundColor Gray

    Write-Host "`nUsage:" -ForegroundColor Cyan
    Write-Host "  .\scripts\test-upgrade.ps1 -AgntPath $OutputDir\agnt.exe -OldBinaryPath $OutputDir\agnt-old.exe"

    exit 0
}
catch {
    Write-Failure $_.Exception.Message

    # Try to return to original branch
    try {
        git checkout $currentBranch 2>$null
    } catch {}

    exit 1
}
finally {
    # Restore stashed changes
    if ($hasChanges) {
        Write-Info "Restoring stashed changes..."
        git stash pop 2>$null
    }
}
