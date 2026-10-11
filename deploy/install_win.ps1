#requires -version 5.1
<#
GPT Admin Windows installer.

Default (no -HubUrl): installs a LOCAL GPT Admin Hub plus its ShellMCP
agent on this computer. All credentials (SHELLMCP_TOKEN, CTL_TOKEN,
ADMIN_PASSWORD, OAUTH_CLIENT_SECRET) are generated here and never leave
this machine. After install, open the admin URL, sign in with the
generated AdminPassword, and approve your own computer ("shell:<name>")
in the pending list. MCP clients connect via the /connect page.

Remote agent mode (explicit non-loopback -HubUrl): installs only the
ShellMCP agent against an existing Hub. The agent generates its own
credential, registers with that Hub, and waits in the Hub pending list
until that Hub's admin approves server "shell:<COMPUTERNAME>".

No git checkout, no private repo, no local Go or Python runtime.
Autostart uses built-in Windows Task Scheduler.

Usage:
  iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
  powershell -ExecutionPolicy Bypass -File .\install_win.ps1 -HubUrl https://became.bezrabotnyi.com -ShellmcpToken srv_secret

Env overrides:
  PACKAGE_URL, GPTADMIN_DIR, HUB_URL, SHELLMCP_TOKEN, CTL_TOKEN,
  ADMIN_PASSWORD, OAUTH_CLIENT_SECRET, GPTADMIN_HUB_PORT, SHELLMCP_PORT,
  SHELLMCP_BIND, SHELLMCP_NAME, SHELLMCP_URL, SHELLMCP_TRANSPORT,
  HUB_PUBLIC_KEY, SHELLMCP_TASK_NAME
#>

param(
    [string]$PackageUrl = $env:PACKAGE_URL,
    [string]$InstallDir = $env:GPTADMIN_DIR,
    [string]$HubUrl = $env:HUB_URL,
    [string]$ShellmcpToken = $env:SHELLMCP_TOKEN,
    [string]$CtlToken = $env:CTL_TOKEN,
    [string]$AdminPassword = $env:ADMIN_PASSWORD,
    [string]$OAuthClientSecret = $env:OAUTH_CLIENT_SECRET,
    [int]$HubPort = $(if ($env:GPTADMIN_HUB_PORT) { [int]$env:GPTADMIN_HUB_PORT } else { 9001 }),
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

function New-RandomToken {
    $bytes = New-Object byte[] 32
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Test-LoopbackUrl {
    param([string]$Url)
    try { $uri = [Uri]$Url } catch { return $false }
    if ($uri.Scheme -notin @('http', 'https')) { return $false }
    return @('127.0.0.1', 'localhost', '::1').Contains($uri.Host)
}

function Get-OwnedProcess {
    param([string]$Name, [string]$Path)
    return @(Get-Process -Name $Name -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and ([System.IO.Path]::GetFullPath($_.Path) -eq [System.IO.Path]::GetFullPath($Path)) } catch { $false }
    })
}

$IsAdmin = Test-Admin
$UserMode = $User -or ((-not $System) -and (-not $IsAdmin))

if (-not $PackageUrl) { $PackageUrl = 'https://became.bezrabotnyi.com/gptadmin-win.zip' }
if (-not $InstallDir) {
    if ($UserMode) { $InstallDir = Join-Path $env:LOCALAPPDATA 'gptadmin' }
    else { $InstallDir = Join-Path $env:ProgramData 'gptadmin' }
}

# A bare install (or an explicit loopback HubUrl) is a self-contained local
# Hub; an explicit non-loopback HubUrl selects remote-agent mode.
$LocalHub = (-not $HubUrl) -or (Test-LoopbackUrl $HubUrl)
if ($LocalHub) { $HubUrl = "http://127.0.0.1:$HubPort" }

$TokenProvided = [bool]$ShellmcpToken
if (-not $ShellmcpToken) { $ShellmcpToken = New-RandomToken }
if ($TokenProvided -and ($ShellmcpToken.Length -lt 16 -or $ShellmcpToken.Length -gt 4096 -or $ShellmcpToken -match '\s|[<>]')) {
    throw 'SHELLMCP_TOKEN must be one non-whitespace line; refusing malformed credential material.'
}
if ($LocalHub) {
    if (-not $CtlToken) { $CtlToken = New-RandomToken }
    if (-not $AdminPassword) { $AdminPassword = New-RandomToken }
    if (-not $OAuthClientSecret) { $OAuthClientSecret = New-RandomToken }
}
if (-not $ShellmcpName) { $ShellmcpName = $env:COMPUTERNAME }
if (-not $ShellmcpUrl) { $ShellmcpUrl = "http://$ShellmcpName`:$ShellmcpPort" }
if (-not $TaskName) {
    if ($UserMode) { $TaskName = "gptadmin-shellmcp-$env:USERNAME" }
    else { $TaskName = 'gptadmin-shellmcp' }
}
$HubTaskName = $TaskName -replace '^gptadmin-shellmcp', 'gptadmin-hub'

$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$BinDir = Join-Path $InstallDir 'bin'
$LogDir = Join-Path $InstallDir 'logs'
$IdentityDir = Join-Path $InstallDir 'identity'
$ConfigDir = Join-Path $InstallDir 'config'
$PublicDir = Join-Path $InstallDir 'public'
$EnvFile = Join-Path $InstallDir 'shellmcp.env'
$HubEnvFile = Join-Path $InstallDir 'hub.env'
$RunScript = Join-Path $InstallDir 'run_shellmcp.ps1'
$BatchScript = Join-Path $InstallDir 'run_shellmcp.cmd'
$HubRunScript = Join-Path $InstallDir 'run_hub.ps1'
$HubBatchScript = Join-Path $InstallDir 'run_hub.cmd'
$CredentialsFile = Join-Path $InstallDir 'ADMIN-CREDENTIALS.txt'
$HubPublicKeyFile = Join-Path $InstallDir 'hub_ed25519.pub'
$CurrentExe = Join-Path $BinDir 'shellmcp.exe'
$HubExe = Join-Path $BinDir 'gptadmin-hub.exe'
$StartupScript = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup\gptadmin-shellmcp.cmd'
$HubStartupScript = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup\gptadmin-hub.cmd'

function Require-Admin {
    if (-not $IsAdmin) {
        throw 'Run PowerShell as Administrator, or use -User for per-user install.'
    }
}

function Download-And-InstallArtifact {
    New-Item -ItemType Directory -Force -Path $InstallDir,$BinDir,$LogDir,$IdentityDir,$ConfigDir | Out-Null
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('gptadmin-win-' + [System.Guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    $archive = Join-Path $tmp 'gptadmin-win.zip'

    if (Test-Path -LiteralPath $PackageUrl) {
        Write-Host "Installing local package $PackageUrl"
        Copy-Item -LiteralPath $PackageUrl -Destination $archive -Force
    } else {
        Write-Host "Downloading $PackageUrl"
        Invoke-WebRequest -UseBasicParsing -Uri $PackageUrl -OutFile $archive
    }
    Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force

    $exe = Get-ChildItem -Path $tmp -Recurse -File -Include 'shellmcp.exe','shellmcp_win.exe' | Select-Object -First 1
    if (-not $exe) { throw 'shellmcp executable not found in package. Expected shellmcp.exe or shellmcp_win.exe.' }
    $hubExe = Get-ChildItem -Path $tmp -Recurse -File -Filter 'gptadmin-hub.exe' | Select-Object -First 1

    $stamp = Get-Date -Format 'yyyyMMdd_HHmmss'
    if ($UserMode) {
        # User installs must not depend on Task Scheduler access. Restricted
        # SSH/UAC tokens can deny or stall ScheduledTasks cmdlets even for the
        # same account. Stop only our own installed processes by path.
        Get-OwnedProcess -Name 'shellmcp' -Path $CurrentExe | Stop-Process -Force -ErrorAction SilentlyContinue
        if ($LocalHub -and $hubExe) { Get-OwnedProcess -Name 'gptadmin-hub' -Path $HubExe | Stop-Process -Force -ErrorAction SilentlyContinue }
        Start-Sleep -Milliseconds 500
    } else {
        if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
            Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 3
        }
        if ($LocalHub -and $hubExe -and (Get-ScheduledTask -TaskName $HubTaskName -ErrorAction SilentlyContinue)) {
            Stop-ScheduledTask -TaskName $HubTaskName -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 1
        }
    }
    if (Test-Path $CurrentExe) {
        Copy-Item $CurrentExe (Join-Path $BinDir "shellmcp.exe.bak.$stamp") -Force
    }
    Copy-Item $exe.FullName $CurrentExe -Force
    if ($LocalHub) {
        if (-not $hubExe) { throw 'gptadmin-hub.exe not found in package; a bare install needs the local Hub binary.' }
        if (Test-Path $HubExe) {
            Copy-Item $HubExe (Join-Path $BinDir "gptadmin-hub.exe.bak.$stamp") -Force
        }
        Copy-Item $hubExe.FullName $HubExe -Force
        # The admin UI and public payloads ride in the same archive.
        foreach ($name in @('admin', 'openapi.yaml', 'custom-gpt-instructions.md')) {
            $src = Get-ChildItem -Path $tmp -Recurse -Filter $name | Select-Object -First 1
            if ($src) {
                New-Item -ItemType Directory -Force -Path $PublicDir | Out-Null
                if ($src.PSIsContainer) {
                    Copy-Item -LiteralPath $src.FullName -Destination (Join-Path $PublicDir $name) -Recurse -Force
                } else {
                    Copy-Item -LiteralPath $src.FullName -Destination (Join-Path $PublicDir $name) -Force
                }
            }
        }
    }
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

function Write-Config {
    Set-Content -Path $HubPublicKeyFile -Value $HubPublicKey -Encoding ASCII
    $queueEnabled = if ($ShellmcpTransport -eq 'polling') { '1' } else { '0' }
    @(
        "SHELLMCP_TOKEN=$ShellmcpToken",
        "HUB_URL=$HubUrl",
        "SHELLMCP_PORT=$ShellmcpPort",
        "SHELLMCP_HOST=$ShellmcpBind",
        "SHELLMCP_TRANSPORT=$ShellmcpTransport",
        "SHELLMCP_URL=$ShellmcpUrl",
        "SHELLMCP_NAME=$ShellmcpName",
        "SHELL_NAME=$ShellmcpName",
        "SHELLMCP_IDENTITY_DIR=$IdentityDir",
        "SHELL_IDENTITY_DIR=$IdentityDir",
        "SHELLMCP_QUEUE=$queueEnabled",
        "HUB_PUBLIC_KEY_FILE=$HubPublicKeyFile",
        "SHELLMCP_SERVICE_NAME=$TaskName",
        "SHELLMCP_SERVICE_SCOPE=$(if ($UserMode) { 'user' } else { 'system' })",
        "SHELLMCP_AUTO_UPDATE=$(if ($LocalHub) { '0' } else { '1' })",
        "SHELLMCP_UPDATE_INTERVAL_S=3600",
        "SHELLMCP_UPDATE_MANIFEST_URL=$HubUrl/artifacts/shellmcp.json",
        "SHELLMCP_UPDATE_TOKEN=$ShellmcpToken"
    ) | Set-Content -Path $EnvFile -Encoding ASCII

    @"
@echo off
for /f "usebackq tokens=1,* delims==" %%A in ("$EnvFile") do set "%%A=%%B"
"$CurrentExe" >> "$LogDir\shellmcp.task.log" 2>&1
"@ | Set-Content -Path $BatchScript -Encoding ASCII

    @"
`$ErrorActionPreference = 'Stop'
`$env:SHELLMCP_TOKEN = '$ShellmcpToken'
`$env:HUB_URL = '$HubUrl'
`$env:SHELLMCP_PORT = '$ShellmcpPort'
`$env:SHELLMCP_HOST = '$ShellmcpBind'
`$env:SHELLMCP_TRANSPORT = '$ShellmcpTransport'
`$env:SHELLMCP_URL = '$ShellmcpUrl'
`$env:SHELLMCP_NAME = '$ShellmcpName'
`$env:SHELL_NAME = '$ShellmcpName'
`$env:SHELLMCP_IDENTITY_DIR = '$IdentityDir'
`$env:SHELL_IDENTITY_DIR = '$IdentityDir'
`$env:SHELLMCP_QUEUE = '$queueEnabled'
`$env:HUB_PUBLIC_KEY_FILE = '$HubPublicKeyFile'
`$env:SHELLMCP_SERVICE_NAME = '$TaskName'
`$env:SHELLMCP_SERVICE_SCOPE = '$(if ($UserMode) { 'user' } else { 'system' })'
`$env:SHELLMCP_AUTO_UPDATE = '$(if ($LocalHub) { '0' } else { '1' })'
`$env:SHELLMCP_UPDATE_INTERVAL_S = '3600'
`$env:SHELLMCP_UPDATE_MANIFEST_URL = '$HubUrl/artifacts/shellmcp.json'
`$env:SHELLMCP_UPDATE_TOKEN = '$ShellmcpToken'
& '$BatchScript'
"@ | Set-Content -Path $RunScript -Encoding UTF8

    if ($LocalHub) {
        # Everything the standalone Hub needs: its own state dir, the admin
        # UI payloads, loopback bind, and the credentials minted above. The
        # agent's SHELLMCP_TOKEN doubles as the Hub's shell credential, so
        # the local agent authenticates without any operator-issued secret.
        @(
            "GPTADMIN_ROOT=$InstallDir",
            "GPTADMIN_CONFIG_DIR=$ConfigDir",
            "GPTADMIN_PUBLIC_DIR=$PublicDir",
            "GPTADMIN_HUB_HOST=127.0.0.1",
            "GPTADMIN_HUB_PORT=$HubPort",
            "PUBLIC_ORIGIN=$HubUrl",
            "GPTADMIN_STANDALONE=1",
            "SHELLMCP_TOKEN=$ShellmcpToken",
            "CTL_TOKEN=$CtlToken",
            "ADMIN_PASSWORD=$AdminPassword",
            "OAUTH_CLIENT_SECRET=$OAuthClientSecret"
        ) | Set-Content -Path $HubEnvFile -Encoding ASCII

        @"
@echo off
for /f "usebackq tokens=1,* delims==" %%A in ("$HubEnvFile") do set "%%A=%%B"
"$HubExe" >> "$LogDir\gptadmin-hub.task.log" 2>&1
"@ | Set-Content -Path $HubBatchScript -Encoding ASCII

        @"
`$ErrorActionPreference = 'Stop'
Get-Content -LiteralPath '$HubEnvFile' | ForEach-Object {
    `$parts = `$_ -split '=', 2
    if (`$parts.Length -eq 2 -and `$parts[0]) { Set-Item -Path ("Env:" + `$parts[0]) -Value `$parts[1] }
}
& '$HubBatchScript'
"@ | Set-Content -Path $HubRunScript -Encoding UTF8

        @(
            "GPT Admin local Hub",
            "Admin URL:  $HubUrl/admin",
            "Connect:    $HubUrl/connect",
            "AdminPassword: $AdminPassword"
        ) | Set-Content -Path $CredentialsFile -Encoding UTF8
    }
}

function Install-UserStartup {
    param([string]$Batch, [string]$Startup)
    New-Item -ItemType Directory -Force -Path (Split-Path $Startup) | Out-Null
    @(
        '@echo off',
        "call `"$Batch`""
    ) | Set-Content -Path $Startup -Encoding ASCII
}

function Install-Task {
    param([string]$Name, [string]$Run, [string]$Batch, [string]$Startup)
    # Per-user startup is deliberately Task-Scheduler-free. This works from
    # normal interactive shells and restricted SSH/UAC tokens alike. System
    # installs still use a real machine Scheduled Task.
    if ($UserMode) {
        Install-UserStartup -Batch $Batch -Startup $Startup
        return 'startup'
    }
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$Run`""
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $Name -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force -ErrorAction Stop | Out-Null
    return 'task'
}

function Wait-HubHealth {
    for ($i = 0; $i -lt 30; $i++) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -Uri "$HubUrl/healthz"
            if ($response.StatusCode -eq 200) { return $true }
        } catch { }
        Start-Sleep -Seconds 1
    }
    return $false
}

if (-not $UserMode) { Require-Admin }

if ($Uninstall) {
    if ($UserMode) {
        Remove-Item $StartupScript,$HubStartupScript -Force -ErrorAction SilentlyContinue
        Get-OwnedProcess -Name 'shellmcp' -Path $CurrentExe | Stop-Process -Force -ErrorAction SilentlyContinue
        Get-OwnedProcess -Name 'gptadmin-hub' -Path $HubExe | Stop-Process -Force -ErrorAction SilentlyContinue
        Write-Host "Removed per-user startup launchers"
    } else {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $HubTaskName -Confirm:$false -ErrorAction SilentlyContinue
        Write-Host "Removed scheduled tasks: $TaskName, $HubTaskName"
    }
    exit 0
}

Download-And-InstallArtifact
Write-Config

if ($LocalHub) {
    $HubBackend = Install-Task -Name $HubTaskName -Run $HubRunScript -Batch $HubBatchScript -Startup $HubStartupScript
    if (-not $NoStart -and $HubBackend -eq 'task') { Start-ScheduledTask -TaskName $HubTaskName }
    if (-not $NoStart -and $HubBackend -eq 'startup') {
        Start-Process -FilePath 'powershell.exe' -WindowStyle Hidden -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $HubRunScript)
    }
    if (-not $NoStart -and -not (Wait-HubHealth)) {
        throw "Local GPT Admin Hub did not become healthy at $HubUrl/healthz. Check $LogDir\gptadmin-hub.task.log"
    }
}

$LaunchBackend = Install-Task -Name $TaskName -Run $RunScript -Batch $BatchScript -Startup $StartupScript
if (-not $NoStart -and $LaunchBackend -eq 'task') { Start-ScheduledTask -TaskName $TaskName }
if (-not $NoStart -and $LaunchBackend -eq 'startup') {
    Start-Process -FilePath 'powershell.exe' -WindowStyle Hidden -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $RunScript)
}
Start-Sleep -Seconds 3
if (-not $NoStart) {
    $live = Get-OwnedProcess -Name 'shellmcp' -Path $CurrentExe
    if ($live.Count -lt 1) {
        throw "ShellMCP did not remain running after install. Check $LogDir\shellmcp.task.log"
    }
    if ($LocalHub) {
        $hubLive = Get-OwnedProcess -Name 'gptadmin-hub' -Path $HubExe
        if ($hubLive.Count -lt 1 -or -not (Wait-HubHealth)) {
            throw "Local GPT Admin Hub did not remain running after install. Check $LogDir\gptadmin-hub.task.log"
        }
    }
}

Write-Host "Installed GPT Admin $(if ($LocalHub) { 'local Hub and ShellMCP' } else { 'ShellMCP (remote agent)' })"
Write-Host "InstallMode: $(if ($UserMode) { 'user' } else { 'system' })"
Write-Host "TaskName: $TaskName$(if ($LocalHub) { ", $HubTaskName" })"
Write-Host "InstallDir: $InstallDir"
Write-Host "PackageUrl: $PackageUrl"
Write-Host "Exe: $CurrentExe$(if ($LocalHub) { ", $HubExe" })"
Write-Host "HubUrl: $HubUrl"
Write-Host "Port: $ShellmcpPort"
Write-Host "ShellmcpUrl: $ShellmcpUrl"
Write-Host "Transport: $ShellmcpTransport"
Write-Host "HubPublicKeyFile: $HubPublicKeyFile"
Write-Host "CredentialSource: $(if ($TokenProvided) { 'provided' } else { 'generated locally' })"
if ($LocalHub) {
    Write-Host ""
    Write-Host "AdminUrl: $HubUrl/admin"
    Write-Host "AdminPassword: $AdminPassword"
    Write-Host "CredentialsFile: $CredentialsFile"
    Write-Host "Status: local Hub is running and your computer is connected to it"
    Write-Host "          (agent 'shell:$ShellmcpName' is trusted automatically on a"
    Write-Host "           single-computer install)."
    Write-Host "MCP clients: use the Connect page at $HubUrl/connect"
} else {
    Write-Host "NextStep: the Hub lists this machine as pending server 'shell:$ShellmcpName'; ask the Hub admin to approve it."
}
if ($ShellmcpTransport -eq 'polling') {
    Write-Host 'Polling mode: local HTTP listener is intentionally disabled.'
} else {
    try {
        Invoke-RestMethod -UseBasicParsing -Uri "http://127.0.0.1:$ShellmcpPort/system/info" -Headers @{ Authorization = "Bearer $ShellmcpToken" } | ConvertTo-Json -Compress | Write-Host
    } catch {
        Write-Warning "Local shellmcp health check failed. Check $LogDir\shellmcp.task.log"
    }
}
