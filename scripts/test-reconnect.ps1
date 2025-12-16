# test-reconnect.ps1
# Windows reconnection tests for agnt daemon

param(
    [string]$AgntPath = "agnt.exe",
    [string]$SocketPath = "$env:TEMP\agnt-test-reconnect.sock"
)

$ErrorActionPreference = "Stop"

# Colors for output
function Write-TestHeader($message) {
    Write-Host "`n=== $message ===" -ForegroundColor Cyan
}

function Write-Success($message) {
    Write-Host "[OK] $message" -ForegroundColor Green
}

function Write-Failure($message) {
    Write-Host "[FAIL] $message" -ForegroundColor Red
}

function Write-Info($message) {
    Write-Host "[INFO] $message" -ForegroundColor Yellow
}

# Helper: Wait for daemon to be ready
function Wait-DaemonReady {
    param([int]$TimeoutSeconds = 5)

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $result = & $AgntPath daemon status --socket $SocketPath 2>$null
            if ($LASTEXITCODE -eq 0) {
                return $true
            }
        } catch {}
        Start-Sleep -Milliseconds 100
    }
    return $false
}

# Helper: Wait for daemon to stop
function Wait-DaemonStopped {
    param([int]$TimeoutSeconds = 5)

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            & $AgntPath daemon status --socket $SocketPath 2>$null
            if ($LASTEXITCODE -ne 0) {
                return $true
            }
        } catch {
            return $true
        }
        Start-Sleep -Milliseconds 100
    }
    return $false
}

# Helper: Start daemon
function Start-TestDaemon {
    Write-Info "Starting daemon on socket: $SocketPath"

    $job = Start-Job -ScriptBlock {
        param($agnt, $sock)
        & $agnt daemon start --socket $sock 2>&1
    } -ArgumentList $AgntPath, $SocketPath

    if (-not (Wait-DaemonReady -TimeoutSeconds 5)) {
        Stop-Job $job -ErrorAction SilentlyContinue
        Remove-Job $job -ErrorAction SilentlyContinue
        throw "Daemon failed to start"
    }

    return $job
}

# Helper: Stop daemon
function Stop-TestDaemon {
    param($job)

    try {
        & $AgntPath daemon stop --socket $SocketPath 2>$null
    } catch {}

    Start-Sleep -Seconds 1

    if ($job) {
        Stop-Job $job -ErrorAction SilentlyContinue
        Remove-Job $job -ErrorAction SilentlyContinue
    }
}

# Helper: Kill daemon process
function Kill-Daemon {
    $processes = Get-Process -Name "agnt*" -ErrorAction SilentlyContinue
    foreach ($proc in $processes) {
        Write-Info "Killing daemon process: $($proc.Id)"
        Stop-Process -Id $proc.Id -Force
    }

    # Wait for processes to be gone
    Start-Sleep -Milliseconds 500
}

# Cleanup before tests
function Cleanup {
    Write-Info "Cleaning up any existing daemon..."

    # Try graceful stop first
    try {
        & $AgntPath daemon stop --socket $SocketPath 2>$null
    } catch {}

    # Kill any remaining processes
    Kill-Daemon

    # Remove socket file
    if (Test-Path $SocketPath) {
        Remove-Item $SocketPath -Force
    }

    Start-Sleep -Seconds 1
}

# Test 1: Kill daemon and verify client reconnects
function Test-KillDaemonReconnect {
    Write-TestHeader "Test 1: Kill daemon and verify client reconnects"

    try {
        # Start daemon
        $daemonJob = Start-TestDaemon
        Write-Success "Daemon started"

        # Get daemon info to verify connection
        $info = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json
        Write-Success "Connected to daemon v$($info.version)"

        # Kill the daemon process (simulate crash)
        Write-Info "Killing daemon process to simulate crash..."
        Kill-Daemon

        if (-not (Wait-DaemonStopped -TimeoutSeconds 3)) {
            throw "Daemon did not stop after kill"
        }
        Write-Success "Daemon process killed"

        # Try to use client - should auto-start daemon
        Write-Info "Attempting to use client (should trigger auto-start)..."
        $info2 = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json

        if ($LASTEXITCODE -ne 0) {
            throw "Client failed to reconnect after daemon kill"
        }

        Write-Success "Client auto-started daemon and reconnected"
        Write-Success "New daemon v$($info2.version) running"

        return $true
    }
    catch {
        Write-Failure $_.Exception.Message
        return $false
    }
    finally {
        Cleanup
    }
}

# Test 2: Graceful daemon restart with client recovery
function Test-GracefulRestart {
    Write-TestHeader "Test 2: Graceful daemon restart with client recovery"

    try {
        # Start daemon
        $daemonJob = Start-TestDaemon
        Write-Success "Daemon started"

        # Get initial info
        $info = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json
        Write-Success "Connected to daemon v$($info.version), uptime: $($info.uptime)"

        # Graceful stop
        Write-Info "Stopping daemon gracefully..."
        & $AgntPath daemon stop --socket $SocketPath 2>&1

        if (-not (Wait-DaemonStopped -TimeoutSeconds 5)) {
            throw "Daemon did not stop gracefully"
        }
        Write-Success "Daemon stopped gracefully"

        # Client should auto-start new daemon
        Write-Info "Attempting to use client (should trigger auto-start)..."
        $info2 = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json

        if ($LASTEXITCODE -ne 0) {
            throw "Client failed to auto-start daemon"
        }

        Write-Success "Client auto-started daemon"
        Write-Success "New daemon v$($info2.version) running"

        return $true
    }
    catch {
        Write-Failure $_.Exception.Message
        return $false
    }
    finally {
        Cleanup
    }
}

# Test 3: Socket file deletion with recovery
function Test-SocketDeletion {
    Write-TestHeader "Test 3: Socket file deletion with recovery"

    try {
        # Start daemon
        $daemonJob = Start-TestDaemon
        Write-Success "Daemon started"

        # Verify connection
        $info = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json
        Write-Success "Connected to daemon v$($info.version)"

        # Delete socket file while daemon running
        Write-Info "Deleting socket file while daemon is running..."
        if (Test-Path $SocketPath) {
            Remove-Item $SocketPath -Force
            Write-Success "Socket file deleted"
        }

        # Try to use client - should fail and auto-restart daemon
        Write-Info "Attempting to use client after socket deletion..."
        $info2 = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json

        if ($LASTEXITCODE -ne 0) {
            throw "Client failed to recover after socket deletion"
        }

        Write-Success "Client detected stale daemon and auto-started new one"
        Write-Success "New daemon v$($info2.version) running"

        return $true
    }
    catch {
        Write-Failure $_.Exception.Message
        return $false
    }
    finally {
        Cleanup
    }
}

# Test 4: Multiple rapid reconnections
function Test-RapidReconnections {
    Write-TestHeader "Test 4: Multiple rapid reconnections"

    try {
        $successCount = 0
        $iterations = 5

        for ($i = 1; $i -le $iterations; $i++) {
            Write-Info "Iteration $i of $iterations"

            # Start daemon
            $daemonJob = Start-TestDaemon

            # Use client
            $info = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json
            if ($LASTEXITCODE -eq 0) {
                Write-Success "Connected on iteration $i"
                $successCount++
            }

            # Stop daemon
            Stop-TestDaemon $daemonJob

            Start-Sleep -Milliseconds 200
        }

        if ($successCount -eq $iterations) {
            Write-Success "All $iterations iterations succeeded"
            return $true
        }
        else {
            Write-Failure "Only $successCount of $iterations iterations succeeded"
            return $false
        }
    }
    catch {
        Write-Failure $_.Exception.Message
        return $false
    }
    finally {
        Cleanup
    }
}

# Test 5: Client operations survive daemon restart
function Test-ClientOperationsDuringRestart {
    Write-TestHeader "Test 5: Client operations survive daemon restart"

    try {
        # Start daemon
        $daemonJob = Start-TestDaemon
        Write-Success "Daemon started"

        # Start a long-running process via client
        Write-Info "Starting a background process..."
        & $AgntPath --socket $SocketPath 2>&1 | Out-Null

        # Kill daemon
        Write-Info "Killing daemon while process running..."
        Kill-Daemon

        if (-not (Wait-DaemonStopped -TimeoutSeconds 3)) {
            throw "Daemon did not stop"
        }

        # Try to query process list - should auto-start daemon
        Write-Info "Querying process list (should trigger auto-start)..."
        $result = & $AgntPath daemon info --socket $SocketPath 2>&1 | ConvertFrom-Json

        if ($LASTEXITCODE -ne 0) {
            throw "Client failed to reconnect"
        }

        Write-Success "Client reconnected and daemon auto-started"
        Write-Success "Daemon v$($result.version) running"

        return $true
    }
    catch {
        Write-Failure $_.Exception.Message
        return $false
    }
    finally {
        Cleanup
    }
}

# Main execution
Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host "   agnt Windows Reconnection Tests" -ForegroundColor Cyan
Write-Host "========================================`n" -ForegroundColor Cyan

Write-Info "Using agnt binary: $AgntPath"
Write-Info "Test socket path: $SocketPath"

# Verify agnt exists
if (-not (Test-Path $AgntPath)) {
    Write-Failure "agnt binary not found at: $AgntPath"
    Write-Info "Build it with: go build -o agnt.exe ./cmd/agnt/"
    exit 1
}

# Initial cleanup
Cleanup

# Run tests
$results = @()
$results += @{ Name = "Kill daemon reconnect"; Passed = (Test-KillDaemonReconnect) }
$results += @{ Name = "Graceful restart"; Passed = (Test-GracefulRestart) }
$results += @{ Name = "Socket deletion recovery"; Passed = (Test-SocketDeletion) }
$results += @{ Name = "Rapid reconnections"; Passed = (Test-RapidReconnections) }
$results += @{ Name = "Operations during restart"; Passed = (Test-ClientOperationsDuringRestart) }

# Final cleanup
Cleanup

# Summary
Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host "           Test Summary" -ForegroundColor Cyan
Write-Host "========================================`n" -ForegroundColor Cyan

$passCount = 0
$failCount = 0

foreach ($result in $results) {
    if ($result.Passed) {
        Write-Success $result.Name
        $passCount++
    }
    else {
        Write-Failure $result.Name
        $failCount++
    }
}

Write-Host "`nTotal: $($results.Count) tests" -ForegroundColor White
Write-Host "Passed: $passCount" -ForegroundColor Green
Write-Host "Failed: $failCount" -ForegroundColor $(if ($failCount -gt 0) { "Red" } else { "Green" })

if ($failCount -gt 0) {
    exit 1
}
else {
    Write-Host "`nAll tests passed!" -ForegroundColor Green
    exit 0
}
