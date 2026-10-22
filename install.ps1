# agnt installer for Windows
# Usage: irm https://raw.githubusercontent.com/standardbeagle/agnt/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$Repo = "standardbeagle/agnt"
$BinaryName = "agnt"

function Get-Platform {
    return "windows"
}

function Get-Arch {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    switch ($arch) {
        "X64" { return "amd64" }
        "Arm64" { return "arm64" }
        default { throw "Unsupported architecture: $arch" }
    }
}

function Get-LatestVersion {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
    return $release.tag_name -replace '^v', ''
}

function Main {
    $platform = Get-Platform
    $arch = Get-Arch
    $version = if ($env:AGNT_VERSION) { $env:AGNT_VERSION } else { Get-LatestVersion }
    $installDir = if ($env:AGNT_INSTALL_DIR) { $env:AGNT_INSTALL_DIR } else { "$env:LOCALAPPDATA\agnt" }

    Write-Host "Installing agnt v$version..."
    Write-Host "  Platform: $platform"
    Write-Host "  Architecture: $arch"
    Write-Host "  Install directory: $installDir"

    # Create install directory
    if (-not (Test-Path $installDir)) {
        New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    }

    # Download URL
    $url = "https://github.com/$Repo/releases/download/v$version/$BinaryName-$platform-$arch.exe"
    $binaryPath = Join-Path $installDir "$BinaryName.exe"

    Write-Host "  Downloading from: $url"

    # Download beside the target, verify, then swap into place. A failed or
    # truncated download never replaces a working binary, and a running agnt.exe
    # (locked against overwrite but not against rename) is moved aside first.
    $tmpPath = "$binaryPath.new"
    $oldPath = "$binaryPath.old"
    Remove-Item $tmpPath -Force -ErrorAction SilentlyContinue
    Invoke-WebRequest -Uri $url -OutFile $tmpPath -UseBasicParsing

    $sumsUrl = "https://github.com/$Repo/releases/download/v$version/SHA256SUMS"
    $sums = $null
    try {
        $sums = (Invoke-WebRequest -Uri $sumsUrl -UseBasicParsing).Content
    } catch {
        $sums = $null
    }
    if ($sums) {
        $asset = "$BinaryName-$platform-$arch.exe"
        $expected = $null
        foreach ($line in ($sums -split "`n")) {
            $parts = $line.Trim() -split '\s+'
            if ($parts.Length -ge 2 -and $parts[1].TrimStart('*') -eq $asset) { $expected = $parts[0] }
        }
        if (-not $expected) {
            Remove-Item $tmpPath -Force -ErrorAction SilentlyContinue
            throw "no checksum for $asset in SHA256SUMS"
        }
        $actual = (Get-FileHash -Algorithm SHA256 -Path $tmpPath).Hash
        if ($actual -ne $expected.ToUpper()) {
            Remove-Item $tmpPath -Force -ErrorAction SilentlyContinue
            throw "checksum mismatch for $asset (expected $expected, got $actual)"
        }
        Write-Host "  Checksum verified"
    } elseif ($env:AGNT_REQUIRE_CHECKSUM -eq "1") {
        Remove-Item $tmpPath -Force -ErrorAction SilentlyContinue
        throw "SHA256SUMS is not published for v$version and AGNT_REQUIRE_CHECKSUM=1"
    } else {
        Write-Warning "no SHA256SUMS published for v$version; skipping checksum verification"
    }

    Remove-Item $oldPath -Force -ErrorAction SilentlyContinue
    if (Test-Path $binaryPath) { Move-Item $binaryPath $oldPath -Force }
    try {
        Move-Item $tmpPath $binaryPath -Force
    } catch {
        if (Test-Path $oldPath) { Move-Item $oldPath $binaryPath -Force }
        throw
    }
    Remove-Item $oldPath -Force -ErrorAction SilentlyContinue

    Write-Host ""
    Write-Host "Successfully installed agnt to $binaryPath"
    Write-Host ""

    # Check if install_dir is in PATH
    $userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($userPath -notlike "*$installDir*") {
        Write-Host "Adding $installDir to user PATH..."
        [Environment]::SetEnvironmentVariable("PATH", "$installDir;$userPath", "User")
        $env:PATH = "$installDir;$env:PATH"
        Write-Host "PATH updated. You may need to restart your terminal."
        Write-Host ""
    }

    # Verify installation
    & $binaryPath --version
}

Main
