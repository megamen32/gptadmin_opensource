# Simple installer for the rootd Windows agent.
# Generates a token, downloads prebuilt package and starts the agent.

$packageUrl = $env:PACKAGE_URL
if (-not $packageUrl) { $packageUrl = "https://became.bezrabotnyi.com/gptadmin-win.zip" }

$token = ([System.Guid]::NewGuid().ToString('n'))
Write-Host "Generated ROOTD token: $token"

$env:ROOTD_TOKEN = $token
if (-not $env:HUB_URL) { $env:HUB_URL = "http://localhost:48653" }

$tmp = New-Item -ItemType Directory -Path ([System.IO.Path]::GetTempPath() + [System.Guid]::NewGuid().ToString())
$archive = Join-Path $tmp "gptadmin-win.zip"
Write-Host "Downloading package..."
Invoke-WebRequest -Uri $packageUrl -OutFile $archive | Out-Null
Expand-Archive -LiteralPath $archive -DestinationPath $tmp
$rootdExe = Join-Path $tmp "rootd_win\dist\rootd_win.exe"

Start-Process -WindowStyle Hidden $rootdExe

Write-Host "rootd Windows agent running and registered to $env:HUB_URL"
Write-Host "Use ROOTD_TOKEN=$token for authorization"
