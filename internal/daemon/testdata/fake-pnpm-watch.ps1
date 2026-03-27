# Fake pnpm watch: binds a TCP port, writes PID file, exits on process termination.
# Usage: fake-pnpm-watch.ps1 <port> <pidfile>
param(
    [Parameter(Mandatory=$true)][int]$Port,
    [Parameter(Mandatory=$true)][string]$PidFile
)

$ErrorActionPreference = 'Stop'

$currentPID = [System.Diagnostics.Process]::GetCurrentProcess().Id
Set-Content -Path $PidFile -Value $currentPID -NoNewline

# Start TCP listener
$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
$listener.Start()

Write-Host "pnpm:watch listening on http://localhost:${Port}"

try {
    # Accept connections in a loop until killed
    while ($true) {
        if ($listener.Pending()) {
            $client = $listener.AcceptTcpClient()
            $stream = $client.GetStream()
            $writer = [System.IO.StreamWriter]::new($stream)
            $writer.WriteLine("pnpm-watch on $currentPID")
            $writer.Flush()
            $client.Close()
        }
        Start-Sleep -Milliseconds 100
    }
} finally {
    $listener.Stop()
    Remove-Item -Path $PidFile -ErrorAction SilentlyContinue
}
