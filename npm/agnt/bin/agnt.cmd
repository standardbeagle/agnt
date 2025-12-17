@echo off
setlocal enabledelayedexpansion

:: agnt wrapper script for Windows
:: Handles binary updates and cleanup before executing

set "SCRIPT_DIR=%~dp0"
set "BINARY=%SCRIPT_DIR%agnt-binary.exe"
set "BINARY_NEW=%BINARY%.new"
set "BINARY_OLD=%BINARY%.old"

:: Cleanup old binary from previous update (may fail if still locked, that's ok)
if exist "%BINARY_OLD%" (
    del /f /q "%BINARY_OLD%" >nul 2>&1
)

:: Apply pending update if exists
if exist "%BINARY_NEW%" (
    :: Rename current to .old (works even if running via daemon)
    if exist "%BINARY%" (
        move /y "%BINARY%" "%BINARY_OLD%" >nul 2>&1
    )
    :: Move new binary into place
    move /y "%BINARY_NEW%" "%BINARY%" >nul 2>&1
    if !errorlevel! equ 0 (
        echo Updated to new version
        :: Try to cleanup old
        del /f /q "%BINARY_OLD%" >nul 2>&1
    )
)

:: Check if binary exists
if not exist "%BINARY%" (
    echo Error: agnt binary not found at %BINARY%
    echo.
    echo The binary may not have been downloaded during installation.
    echo Try reinstalling the package:
    echo   npm uninstall -g @standardbeagle/agnt
    echo   npm install -g @standardbeagle/agnt
    exit /b 1
)

:: Execute the binary with all arguments
"%BINARY%" %*
exit /b %errorlevel%
