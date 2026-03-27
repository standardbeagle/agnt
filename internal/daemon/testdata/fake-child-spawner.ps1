# Fake child spawner: starts a child process that also listens on a port.
# Simulates dotnet watch spawning a child server.
# Usage: fake-child-spawner.ps1 <port> <pidfile>
param(
    [Parameter(Mandatory=$true)][int]$Port,
    [Parameter(Mandatory=$true)][string]$PidFile
)

$ErrorActionPreference = 'Stop'

$currentPID = [System.Diagnostics.Process]::GetCurrentProcess().Id
Set-Content -Path $PidFile -Value $currentPID -NoNewline

# Spawn a child PowerShell process that binds the port
$childScript = @"
`$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
`$listener.Start()
try {
    while (`$true) {
        if (`$listener.Pending()) {
            `$client = `$listener.AcceptTcpClient()
            `$stream = `$client.GetStream()
            `$writer = [System.IO.StreamWriter]::new(`$stream)
            `$writer.WriteLine('child-server on ' + `$PID.Id)
            `$writer.Flush()
            `$client.Close()
        }
        Start-Sleep -Milliseconds 100
    }
} finally {
    `$listener.Stop()
}
"@

$child = Start-Process -FilePath "powershell.exe" -ArgumentList "-NoProfile", "-Command", $childScript -PassThru -NoNewWindow

Write-Host "dotnet-watch parent=$currentPID child=$($child.Id) listening on http://localhost:${Port}"

try {
    # Wait for child to exit (or we get killed)
    $child.WaitForExit()
} finally {
    if (-not $child.HasExited) {
        $child.Kill()
    }
    Remove-Item -Path $PidFile -ErrorAction SilentlyContinue
}
