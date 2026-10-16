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

    # Download URLs
    $asset = "$BinaryName-$platform-$arch.exe"
    $base = "https://github.com/$Repo/releases/download/v$version"
    $url = "$base/$asset"
    $binaryPath = Join-Path $installDir "$BinaryName.exe"

    # Stage beside the install path; nothing reaches $binaryPath until the
    # release's SHA256SUMS vouches for it.
    $tmpDir = Join-Path $installDir (".agnt-install." + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        $sumsPath = Join-Path $tmpDir "SHA256SUMS"
        try {
            Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile $sumsPath -UseBasicParsing
        } catch {
            throw "Could not fetch SHA256SUMS for v$version ($($_.Exception.Message)); refusing to install an unverified binary"
        }
        $expected = $null
        foreach ($line in Get-Content $sumsPath) {
            $fields = -split $line
            if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $asset) {
                $expected = $fields[0].ToLowerInvariant()
                break
            }
        }
        if ($expected -notmatch '^[0-9a-f]{64}$') {
            throw "SHA256SUMS for v$version lists no valid entry for $asset; refusing to install"
        }

        Write-Host "  Downloading from: $url"
        $tmpBinary = Join-Path $tmpDir $asset
        Invoke-WebRequest -Uri $url -OutFile $tmpBinary -UseBasicParsing

        $actual = (Get-FileHash -Algorithm SHA256 -Path $tmpBinary).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            throw "Checksum mismatch for ${asset}: downloaded file has sha256 $actual, SHA256SUMS lists $expected; not installed"
        }
        Write-Host "  Verified sha256: $actual"

        Move-Item -Force -Path $tmpBinary -Destination $binaryPath
    } finally {
        Remove-Item -Recurse -Force -Path $tmpDir -ErrorAction SilentlyContinue
    }

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
