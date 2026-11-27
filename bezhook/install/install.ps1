# bezhook installer for Windows (PowerShell 5.1+). One command:
#   iwr -useb https://dl.t.became.bezrabotnyi.com/bezhook/install.ps1 | iex
# Overrides: -Mirror, -InstallDir
param(
  [string]$Mirror = "https://dl.t.became.bezrabotnyi.com/bezhook",
  [string]$InstallDir = "$env:USERPROFILE\.local\bin"
)

$ErrorActionPreference = "Stop"

switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { $arch = "amd64" }
  "ARM64" { $arch = "arm64" }
  default { throw "bezhook: unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}

$binName = "bezhook-windows-$arch.exe"
$dest = Join-Path $InstallDir "bezhook.exe"

Write-Host "bezhook: downloading $Mirror/dl/$binName ..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) $binName
Invoke-WebRequest -UseBasicParsing -Uri "$Mirror/dl/$binName" -OutFile $tmp

# Verify checksum when possible.
try {
  $sumsFile = Join-Path ([System.IO.Path]::GetTempPath()) "bezhook-SHA256SUMS"
  Invoke-WebRequest -UseBasicParsing -Uri "$Mirror/dl/SHA256SUMS" -OutFile $sumsFile
  $want = (Get-Content $sumsFile | Where-Object { $_ -match " $binName$" }) -replace '\s.*',''
  if ($want) {
    $hash = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash.ToLower()
    if ($hash -ne $want.ToLower()) { throw "bezhook: checksum mismatch for $binName" }
  }
} catch {
  Write-Host "bezhook: checksum verification skipped ($($_.Exception.Message))"
}

Move-Item -Force $tmp $dest

# Make sure $InstallDir is on the user PATH.
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
  Write-Host "bezhook: added $InstallDir to your user PATH (open a new terminal)"
}

& $dest --version
Write-Host ""
Write-Host "Usage: bezhook <local-port> [--sub name] [--ttl 72h]"
Write-Host "  e.g.  bezhook 3000 --sub myapp"
Write-Host "  ->    https://myapp.t.became.bezrabotnyi.com"
