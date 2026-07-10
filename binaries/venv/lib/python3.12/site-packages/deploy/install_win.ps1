#requires -version 5.1
<#
GPT Admin shellmcp Windows public installer.

Installs the obfuscated PyInstaller/PyArmor Windows artifact from a public URL.
No git checkout, no private repo, no local Python runtime.
Autostart uses built-in Windows Task Scheduler.

Usage:
  powershell -ExecutionPolicy Bypass -File .\install_win.ps1 -HubUrl https://gptadmin.bezrabotnyi.com -ShellmcpToken srv_secret
  powershell -ExecutionPolicy Bypass -Command "irm https://became.bezrabotnyi.com/install_win.ps1 -OutFile $env:TEMP\install_win.ps1; powershell -ExecutionPolicy Bypass -File $env:TEMP\install_win.ps1 -HubUrl https://gptadmin.bezrabotnyi.com -ShellmcpToken srv_secret"

Env overrides:
  PACKAGE_URL, GPTADMIN_DIR, HUB_URL, SHELLMCP_TOKEN, SHELLMCP_PORT, SHELLMCP_BIND, SHELLMCP_NAME, SHELLMCP_URL, SHELLMCP_TRANSPORT, HUB_PUBLIC_KEY, SHELLMCP_TASK_NAME
#>

param(
    [string]$PackageUrl = $env:PACKAGE_URL,
    [string]$InstallDir = $env:GPTADMIN_DIR,
    [string]$HubUrl = $env:HUB_URL,
    [string]$ShellmcpToken = $env:SHELLMCP_TOKEN,
    [int]$ShellmcpPort = $(if ($env:SHELLMCP_PORT) { [int]$env:SHELLMCP_PORT } else { 25900 }),
    [string]$ShellmcpBind = $(if ($env:SHELLMCP_BIND) { $env:SHELLMCP_BIND } else { '0.0.0.0' }),
    [string]$ShellmcpName = $env:SHELLMCP_NAME,
    [string]$ShellmcpUrl = $env:SHELLMCP_URL,
    [string]$ShellmcpTransport = $(if ($env:SHELLMCP_TRANSPORT) { $env:SHELLMCP_TRANSPORT } else { 'polling' }),
    [string]$HubPublicKey = $(if ($env:HUB_PUBLIC_KEY) { $env:HUB_PUBLIC_KEY } else { 'mEhYDiOc9ZxY54dXelDTsVD2Wjew3f6R0f2dVW2qKPQ' }),
    [string]$TaskName = $env:SHELLMCP_TASK_NAME,
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
if (-not $ShellmcpToken) { $ShellmcpToken = ([System.Guid]::NewGuid().ToString('n')) }
if (-not $ShellmcpName) { $ShellmcpName = $env:COMPUTERNAME }
if (-not $ShellmcpUrl) { $ShellmcpUrl = "http://$ShellmcpName`:$ShellmcpPort" }
if (-not $TaskName) {
    if ($UserMode) { $TaskName = "gptadmin-shellmcp-$env:USERNAME" }
    else { $TaskName = 'gptadmin-shellmcp' }
}

$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$BinDir = Join-Path $InstallDir 'bin'
$LogDir = Join-Path $InstallDir 'logs'
$EnvFile = Join-Path $InstallDir 'shellmcp.env'
$RunScript = Join-Path $InstallDir 'run_shellmcp.ps1'
$HubPublicKeyFile = Join-Path $InstallDir 'hub_ed25519.pub'
$CurrentExe = Join-Path $BinDir 'shellmcp.exe'

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

    $exe = Get-ChildItem -Path $tmp -Recurse -File -Include 'shellmcp.exe','shellmcp_win.exe' | Select-Object -First 1
    if (-not $exe) { throw 'shellmcp executable not found in package. Expected shellmcp.exe or shellmcp_win.exe.' }

    $stamp = Get-Date -Format 'yyyyMMdd_HHmmss'
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 3
    }
    if (Test-Path $CurrentExe) {
        Copy-Item $CurrentExe (Join-Path $BinDir "shellmcp.exe.bak.$stamp") -Force
    }
    Copy-Item $exe.FullName $CurrentExe -Force
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

function Write-Config {
    Set-Content -Path $HubPublicKeyFile -Value $HubPublicKey -Encoding ASCII
    @(
        "SHELLMCP_TOKEN=$ShellmcpToken",
        "HUB_URL=$HubUrl",
        "SHELLMCP_PORT=$ShellmcpPort",
        "SHELLMCP_BIND=$ShellmcpBind",
        "SHELLMCP_TRANSPORT=$ShellmcpTransport",
        "SHELLMCP_URL=$ShellmcpUrl",
        "SHELLMCP_NAME=$ShellmcpName",
        "QUEUE_URL=1",
        "HUB_PUBLIC_KEY_FILE=$HubPublicKeyFile",
        "SHELLMCP_SERVICE_NAME=$TaskName",
        "SHELLMCP_SERVICE_SCOPE=$(if ($UserMode) { 'user' } else { 'system' })",
        "SHELLMCP_AUTO_UPDATE=1",
        "SHELLMCP_UPDATE_INTERVAL_S=3600",
        "SHELLMCP_UPDATE_MANIFEST_URL=$HubUrl/artifacts/shellmcp.json",
        "SHELLMCP_UPDATE_TOKEN=$ShellmcpToken"
    ) | Set-Content -Path $EnvFile -Encoding ASCII

    @"
`$ErrorActionPreference = 'Stop'
`$env:SHELLMCP_TOKEN = '$ShellmcpToken'
`$env:HUB_URL = '$HubUrl'
`$env:SHELLMCP_PORT = '$ShellmcpPort'
`$env:SHELLMCP_BIND = '$ShellmcpBind'
`$env:SHELLMCP_TRANSPORT = '$ShellmcpTransport'
`$env:SHELLMCP_URL = '$ShellmcpUrl'
`$env:SHELLMCP_NAME = '$ShellmcpName'
`$env:QUEUE_URL = '1'
`$env:HUB_PUBLIC_KEY_FILE = '$HubPublicKeyFile'
`$env:SHELLMCP_SERVICE_NAME = '$TaskName'
`$env:SHELLMCP_SERVICE_SCOPE = '$(if ($UserMode) { 'user' } else { 'system' })'
`$env:SHELLMCP_AUTO_UPDATE = '1'
`$env:SHELLMCP_UPDATE_INTERVAL_S = '3600'
`$env:SHELLMCP_UPDATE_MANIFEST_URL = '$HubUrl/artifacts/shellmcp.json'
`$env:SHELLMCP_UPDATE_TOKEN = '$ShellmcpToken'
Set-Location '$InstallDir'
& '$CurrentExe' *> '$LogDir\shellmcp.task.log'
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

Write-Host "Installed GPT Admin shellmcp"
Write-Host "InstallMode: $(if ($UserMode) { 'user' } else { 'system' })"
Write-Host "TaskName: $TaskName"
Write-Host "InstallDir: $InstallDir"
Write-Host "PackageUrl: $PackageUrl"
Write-Host "Exe: $CurrentExe"
Write-Host "HubUrl: $HubUrl"
Write-Host "Port: $ShellmcpPort"
Write-Host "ShellmcpUrl: $ShellmcpUrl"
Write-Host "Transport: $ShellmcpTransport"
Write-Host "HubPublicKeyFile: $HubPublicKeyFile"
Write-Host "Token: $ShellmcpToken"
if ($ShellmcpTransport -eq 'polling') {
    Write-Host 'Polling mode: local HTTP listener is intentionally disabled.'
} else {
    try {
        Invoke-RestMethod -UseBasicParsing -Uri "http://127.0.0.1:$ShellmcpPort/system/info" -Headers @{ Authorization = "Bearer $ShellmcpToken" } | ConvertTo-Json -Compress | Write-Host
    } catch {
        Write-Warning "Local shellmcp health check failed. Check $LogDir\shellmcp.task.log"
    }
}
