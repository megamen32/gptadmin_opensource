#requires -version 5.1
<#
GPT Admin rootd Windows public installer.

Installs the obfuscated PyInstaller/PyArmor Windows artifact from a public URL.
No git checkout, no private repo, no local Python runtime.
Autostart uses built-in Windows Task Scheduler.

Usage:
  powershell -ExecutionPolicy Bypass -File .\install_win.ps1 -HubUrl https://gptadmin.bezrabotnyi.com -RootdToken srv_secret
  powershell -ExecutionPolicy Bypass -Command "irm https://became.bezrabotnyi.com/install_win.ps1 -OutFile $env:TEMP\install_win.ps1; powershell -ExecutionPolicy Bypass -File $env:TEMP\install_win.ps1 -HubUrl https://gptadmin.bezrabotnyi.com -RootdToken srv_secret"

Env overrides:
  PACKAGE_URL, GPTADMIN_DIR, HUB_URL, ROOTD_TOKEN, ROOTD_PORT, ROOTD_BIND, ROOTD_NAME, ROOTD_URL, ROOTD_TRANSPORT, HUB_PUBLIC_KEY, ROOTD_TASK_NAME
#>

param(
    [string]$PackageUrl = $env:PACKAGE_URL,
    [string]$InstallDir = $env:GPTADMIN_DIR,
    [string]$HubUrl = $env:HUB_URL,
    [string]$RootdToken = $env:ROOTD_TOKEN,
    [int]$RootdPort = $(if ($env:ROOTD_PORT) { [int]$env:ROOTD_PORT } else { 25900 }),
    [string]$RootdBind = $(if ($env:ROOTD_BIND) { $env:ROOTD_BIND } else { '0.0.0.0' }),
    [string]$RootdName = $env:ROOTD_NAME,
    [string]$RootdUrl = $env:ROOTD_URL,
    [string]$RootdTransport = $(if ($env:ROOTD_TRANSPORT) { $env:ROOTD_TRANSPORT } else { 'polling' }),
    [string]$HubPublicKey = $(if ($env:HUB_PUBLIC_KEY) { $env:HUB_PUBLIC_KEY } else { 'mEhYDiOc9ZxY54dXelDTsVD2Wjew3f6R0f2dVW2qKPQ' }),
    [string]$TaskName = $env:ROOTD_TASK_NAME,
    [switch]$User,
    [switch]$System,
    [switch]$Uninstall,
    [switch]$NoStart
)

$ErrorActionPreference = 'Stop'

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($id)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

$IsAdmin = Test-Admin
$UserMode = $User -or ((-not $System) -and (-not $IsAdmin))

if (-not $PackageUrl) { $PackageUrl = 'https://became.bezrabotnyi.com/gptadmin-win.zip' }
if (-not $InstallDir) {
    if ($UserMode) { $InstallDir = Join-Path $env:LOCALAPPDATA 'gptadmin' }
    else { $InstallDir = Join-Path $env:ProgramData 'gptadmin' }
}
if (-not $HubUrl) { $HubUrl = 'https://gptadmin.bezrabotnyi.com' }
if (-not $RootdToken) { $RootdToken = ([System.Guid]::NewGuid().ToString('n')) }
if (-not $RootdName) { $RootdName = $env:COMPUTERNAME }
if (-not $RootdUrl) { $RootdUrl = "http://$RootdName`:$RootdPort" }
if (-not $TaskName) {
    if ($UserMode) { $TaskName = "gptadmin-rootd-$env:USERNAME" }
    else { $TaskName = 'gptadmin-rootd' }
}

$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$BinDir = Join-Path $InstallDir 'bin'
$LogDir = Join-Path $InstallDir 'logs'
$EnvFile = Join-Path $InstallDir 'rootd.env'
$RunScript = Join-Path $InstallDir 'run_rootd.ps1'
$HubPublicKeyFile = Join-Path $InstallDir 'hub_ed25519.pub'
$CurrentExe = Join-Path $BinDir 'rootd.exe'

function Require-Admin {
    if (-not $IsAdmin) {
        throw 'Run PowerShell as Administrator, or use -User for per-user install.'
    }
}

function Download-And-InstallArtifact {
    New-Item -ItemType Directory -Force -Path $InstallDir,$BinDir,$LogDir | Out-Null
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('gptadmin-win-' + [System.Guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    $archive = Join-Path $tmp 'gptadmin-win.zip'

    Write-Host "Downloading $PackageUrl"
    Invoke-WebRequest -UseBasicParsing -Uri $PackageUrl -OutFile $archive
    Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force

    $exe = Get-ChildItem -Path $tmp -Recurse -File -Include 'rootd.exe','rootd_win.exe' | Select-Object -First 1
    if (-not $exe) { throw 'rootd executable not found in package. Expected rootd.exe or rootd_win.exe.' }

    $stamp = Get-Date -Format 'yyyyMMdd_HHmmss'
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 3
    }
    if (Test-Path $CurrentExe) {
        Copy-Item $CurrentExe (Join-Path $BinDir "rootd.exe.bak.$stamp") -Force
    }
    Copy-Item $exe.FullName $CurrentExe -Force
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

function Write-Config {
    Set-Content -Path $HubPublicKeyFile -Value $HubPublicKey -Encoding ASCII
    @(
        "ROOTD_TOKEN=$RootdToken",
        "HUB_URL=$HubUrl",
        "ROOTD_PORT=$RootdPort",
        "ROOTD_BIND=$RootdBind",
        "ROOTD_TRANSPORT=$RootdTransport",
        "ROOTD_URL=$RootdUrl",
        "ROOTD_NAME=$RootdName",
        "QUEUE_URL=1",
        "HUB_PUBLIC_KEY_FILE=$HubPublicKeyFile",
        "ROOTD_SERVICE_NAME=$TaskName",
        "ROOTD_SERVICE_SCOPE=$(if ($UserMode) { 'user' } else { 'system' })",
        "ROOTD_AUTO_UPDATE=1",
        "ROOTD_UPDATE_INTERVAL_S=3600",
        "ROOTD_UPDATE_MANIFEST_URL=$HubUrl/artifacts/rootd.json",
        "ROOTD_UPDATE_TOKEN=$RootdToken"
    ) | Set-Content -Path $EnvFile -Encoding ASCII

    @"
`$ErrorActionPreference = 'Stop'
`$env:ROOTD_TOKEN = '$RootdToken'
`$env:HUB_URL = '$HubUrl'
`$env:ROOTD_PORT = '$RootdPort'
`$env:ROOTD_BIND = '$RootdBind'
`$env:ROOTD_TRANSPORT = '$RootdTransport'
`$env:ROOTD_URL = '$RootdUrl'
`$env:ROOTD_NAME = '$RootdName'
`$env:QUEUE_URL = '1'
`$env:HUB_PUBLIC_KEY_FILE = '$HubPublicKeyFile'
`$env:ROOTD_SERVICE_NAME = '$TaskName'
`$env:ROOTD_SERVICE_SCOPE = '$(if ($UserMode) { 'user' } else { 'system' })'
`$env:ROOTD_AUTO_UPDATE = '1'
`$env:ROOTD_UPDATE_INTERVAL_S = '3600'
`$env:ROOTD_UPDATE_MANIFEST_URL = '$HubUrl/artifacts/rootd.json'
`$env:ROOTD_UPDATE_TOKEN = '$RootdToken'
Set-Location '$InstallDir'
& '$CurrentExe' *> '$LogDir\rootd.task.log'
"@ | Set-Content -Path $RunScript -Encoding UTF8
}

function Install-Task {
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$RunScript`""
    if ($UserMode) {
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
        $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
    } else {
        $trigger = New-ScheduledTaskTrigger -AtStartup
        $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
    }
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
}

if (-not $UserMode) { Require-Admin }

if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "Removed scheduled task: $TaskName"
    exit 0
}

Download-And-InstallArtifact
Write-Config
Install-Task
if (-not $NoStart) { Start-ScheduledTask -TaskName $TaskName }
Start-Sleep -Seconds 3

Write-Host "Installed GPT Admin rootd"
Write-Host "InstallMode: $(if ($UserMode) { 'user' } else { 'system' })"
Write-Host "TaskName: $TaskName"
Write-Host "InstallDir: $InstallDir"
Write-Host "PackageUrl: $PackageUrl"
Write-Host "Exe: $CurrentExe"
Write-Host "HubUrl: $HubUrl"
Write-Host "Port: $RootdPort"
Write-Host "RootdUrl: $RootdUrl"
Write-Host "Transport: $RootdTransport"
Write-Host "HubPublicKeyFile: $HubPublicKeyFile"
Write-Host "Token: $RootdToken"
if ($RootdTransport -eq 'polling') {
    Write-Host 'Polling mode: local HTTP listener is intentionally disabled.'
} else {
    try {
        Invoke-RestMethod -UseBasicParsing -Uri "http://127.0.0.1:$RootdPort/system/info" -Headers @{ Authorization = "Bearer $RootdToken" } | ConvertTo-Json -Compress | Write-Host
    } catch {
        Write-Warning "Local rootd health check failed. Check $LogDir\rootd.task.log"
    }
}
