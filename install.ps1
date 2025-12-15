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

    # Download binary
    Invoke-WebRequest -Uri $url -OutFile $binaryPath -UseBasicParsing

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
