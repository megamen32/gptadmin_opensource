$ErrorActionPreference = 'Stop'

$hub = 'http://192.168.2.100:9001/api/v1/cloud-os'
$stateDir = Join-Path $env:LOCALAPPDATA 'GPTAdminCloudOS'
$stateFile = Join-Path $stateDir 'computer-id.txt'
New-Item -ItemType Directory -Force -Path $stateDir | Out-Null

function Pair-Computer {
    $payload = @{ name = 'Windows BeyondInfinity'; os = 'windows'; capabilities = @('status'); session_id = $env:COMPUTERNAME } | ConvertTo-Json -Compress
    $result = Invoke-RestMethod -Method Post -Uri "$hub/computers/pair" -ContentType 'application/json' -Body $payload -TimeoutSec 10
    Set-Content -Path $stateFile -Value $result.computer.id -NoNewline
}

try {
    $computerID = if (Test-Path $stateFile) { (Get-Content -Raw $stateFile).Trim() } else { '' }
    if (-not $computerID) { Pair-Computer; exit 0 }
    Invoke-RestMethod -Method Post -Uri "$hub/computers/$computerID/heartbeat" -TimeoutSec 10 | Out-Null
} catch {
    Pair-Computer
}
