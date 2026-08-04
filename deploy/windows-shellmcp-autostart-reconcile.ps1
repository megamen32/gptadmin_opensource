#requires -version 5.1
[CmdletBinding()]
param(
    [string]$PackageUrl = 'https://github.com/megamen32/gptadmin_opensource/releases/download/v141/gptadmin-win.zip',
    [string]$InstallDir = "$env:ProgramData\gptadmin",
    [string]$HubUrl,
    [string]$TaskName = 'gptadmin-shellmcp',
    [string[]]$LegacyTaskNames = @('gptadmin-rootd', 'GPTAdminRootd'),
    [string]$ExpectedSha256 = '0f6428a10e3ceccd165d36528e8f050c7e803eb2a80548bb8a6496616b8358fb',
    [int]$ExpectedBuild = 141,
    [string]$ReceiptRoot = "$env:ProgramData\gptadmin\rollback\windows-shellmcp-autostart",
    [string]$RollbackReceipt,
    [string]$FallbackLauncherPath,
    [switch]$AgentTokenFromStdin,
    [switch]$NoStart,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$LegacyUnrelatedTask = 'GPTAdmin MCP BeyondInfinity-windows-mcp'

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Read-EnvMap([string]$Path) {
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
        $parts = $line -split '=', 2
        $values[$parts[0].Trim()] = $parts[1]
    }
    return $values
}

function Get-StringSha256([string]$Value) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Value)))).Replace('-', '').ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

function Get-TaskReceipt([string]$Name, [string]$Directory) {
    $task = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
    if ($null -eq $task) {
        return [ordered]@{ name = $Name; existed = $false; enabled = $false; state = 'Absent'; xml = $null }
    }
    $safeName = $Name -replace '[^A-Za-z0-9_.-]', '_'
    $xmlPath = Join-Path $Directory "$safeName.xml"
    Export-ScheduledTask -TaskName $Name | Set-Content -LiteralPath $xmlPath -Encoding UTF8
    return [ordered]@{
        name = $Name
        existed = $true
        enabled = [bool]$task.Settings.Enabled
        state = [string]$task.State
        xml = $xmlPath
    }
}

function Copy-Backup([string]$Path, [string]$BackupDirectory) {
    if (-not (Test-Path -LiteralPath $Path)) {
        return [ordered]@{ path = $Path; existed = $false; backup = $null; sha256 = $null; sddl = $null }
    }
    $name = Split-Path -Leaf $Path
    $target = Join-Path $BackupDirectory $name
    Copy-Item -LiteralPath $Path -Destination $target -Force
    return [ordered]@{
        path = $Path
        existed = $true
        backup = $target
        sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
        sddl = (Get-Acl -LiteralPath $Path).Sddl
    }
}

function Set-PrivatePathAcl([string]$Path) {
    $item = Get-Item -LiteralPath $Path
    if ($item.PSIsContainer) {
        $acl = New-Object System.Security.AccessControl.DirectorySecurity
        $inheritance = [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    } else {
        $acl = New-Object System.Security.AccessControl.FileSecurity
        $inheritance = [System.Security.AccessControl.InheritanceFlags]::None
    }
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sidValue in @('S-1-5-18', 'S-1-5-32-544')) {
        $sid = New-Object System.Security.Principal.SecurityIdentifier($sidValue)
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            $sid,
            [System.Security.AccessControl.FileSystemRights]::FullControl,
            $inheritance,
            [System.Security.AccessControl.PropagationFlags]::None,
            [System.Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Set-SecretFileAcl([string]$Path) {
    Set-PrivatePathAcl -Path $Path
}

function Stop-CanonicalOwner([string]$TaskName, [string]$ExecutablePath) {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($task) {
        Get-ScheduledTaskInfo -TaskName $TaskName -ErrorAction SilentlyContinue | Out-Null
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    }
    foreach ($process in @(Get-CimInstance Win32_Process -Filter "Name='shellmcp.exe'" -ErrorAction SilentlyContinue)) {
        if ($process.ExecutablePath -and $process.ExecutablePath.Equals($ExecutablePath, [StringComparison]::OrdinalIgnoreCase)) {
            Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
        }
    }
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        $runningTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        $runningProcess = @(Get-CimInstance Win32_Process -Filter "Name='shellmcp.exe'" -ErrorAction SilentlyContinue | Where-Object {
            $_.ExecutablePath -and $_.ExecutablePath.Equals($ExecutablePath, [StringComparison]::OrdinalIgnoreCase)
        })
        if ((-not $runningTask -or $runningTask.State -ne 'Running') -and $runningProcess.Count -eq 0) { return }
        Start-Sleep -Milliseconds 250
    }
    throw 'failed to stop canonical task before rollback'
}

function Invoke-Rollback([string]$ReceiptPath) {
    if (-not (Test-Path -LiteralPath $ReceiptPath)) { throw "rollback receipt not found: $ReceiptPath" }
    $receipt = Get-Content -LiteralPath $ReceiptPath -Raw | ConvertFrom-Json
    $installedExe = if ($receipt.PSObject.Properties['installed_exe'] -and $receipt.installed_exe) {
        [string]$receipt.installed_exe
    } else {
        Join-Path $receipt.install_dir 'bin\shellmcp.exe'
    }
    Stop-CanonicalOwner -TaskName $receipt.canonical_task -ExecutablePath $installedExe
    foreach ($backup in @($receipt.backups)) {
        if (-not $backup) { continue }
        $existedBefore = if ($backup.PSObject.Properties['existed']) { [bool]$backup.existed } else { [bool]$backup.backup }
        if ($existedBefore) {
            if (-not (Test-Path -LiteralPath $backup.backup)) { throw "rollback backup missing: $($backup.backup)" }
            Copy-Item -LiteralPath $backup.backup -Destination $backup.path -Force
            if ($backup.sddl) {
                $restoredAcl = Get-Acl -LiteralPath $backup.path
                $restoredAcl.SetSecurityDescriptorSddlForm([string]$backup.sddl)
                Set-Acl -LiteralPath $backup.path -AclObject $restoredAcl
            }
        } elseif (Test-Path -LiteralPath $backup.path) {
            Remove-Item -LiteralPath $backup.path -Force
        }
    }
    if ($receipt.canonical_before.existed -and (Test-Path -LiteralPath $receipt.canonical_before.xml)) {
        $canonicalXml = Get-Content -LiteralPath $receipt.canonical_before.xml -Raw
        Register-ScheduledTask -TaskName $receipt.canonical_task -Xml $canonicalXml -Force | Out-Null
        if ($receipt.canonical_before.enabled) {
            Enable-ScheduledTask -TaskName $receipt.canonical_task | Out-Null
        } else {
            Disable-ScheduledTask -TaskName $receipt.canonical_task | Out-Null
        }
        if ($receipt.canonical_before.state -eq 'Running') {
            Start-ScheduledTask -TaskName $receipt.canonical_task
        }
    } else {
        Unregister-ScheduledTask -TaskName $receipt.canonical_task -Confirm:$false -ErrorAction SilentlyContinue
    }
    foreach ($legacy in @($receipt.legacy_tasks)) {
        if (-not $legacy.existed) { continue }
        if ($legacy.enabled) {
            Enable-ScheduledTask -TaskName $legacy.name | Out-Null
        } else {
            Disable-ScheduledTask -TaskName $legacy.name | Out-Null
        }
        if ($legacy.state -eq 'Running') {
            Start-ScheduledTask -TaskName $legacy.name
        }
    }
    if ($receipt.fallback_launcher -and (Test-Path -LiteralPath $receipt.fallback_launcher)) {
        if ([IO.Path]::GetExtension([string]$receipt.fallback_launcher) -eq '.ps1') {
            Start-Process -FilePath 'powershell.exe' -ArgumentList @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', [string]$receipt.fallback_launcher) -WindowStyle Hidden
        } else {
            Start-Process -FilePath ([string]$receipt.fallback_launcher) -WindowStyle Hidden
        }
    }
    [ordered]@{
        status = 'rollback_complete'
        receipt = $ReceiptPath
        canonical_task = $receipt.canonical_task
        legacy_tasks_restored = @($receipt.legacy_tasks | ForEach-Object { $_.name })
    } | ConvertTo-Json -Depth 5
}

if ($RollbackReceipt) {
    if (-not (Test-Administrator)) { throw 'Administrator privileges are required for rollback.' }
    Invoke-Rollback -ReceiptPath $RollbackReceipt
    exit 0
}

$BinDir = Join-Path $InstallDir 'bin'
$LogDir = Join-Path $InstallDir 'logs'
$EnvFile = Join-Path $InstallDir 'shellmcp.env'
$RootdEnvFile = Join-Path $InstallDir 'rootd.env'
$RunScript = Join-Path $InstallDir 'run_shellmcp.ps1'
$CurrentExe = Join-Path $BinDir 'shellmcp.exe'
$HubPublicKeyFile = Join-Path $InstallDir 'hub_ed25519.pub'
$existing = Read-EnvMap -Path $EnvFile
$legacy = Read-EnvMap -Path $RootdEnvFile
$stdinToken = $null
if ($AgentTokenFromStdin) {
    $stdinToken = [Console]::In.ReadLine()
    if (-not $stdinToken) { throw 'Current agent credential was not provided on stdin.' }
}
$runtimeToken = if ($stdinToken) { $stdinToken } elseif ($existing.ContainsKey('SHELLMCP_TOKEN')) { $existing['SHELLMCP_TOKEN'] } elseif ($legacy.ContainsKey('ROOTD_TOKEN')) { $legacy['ROOTD_TOKEN'] } else { $null }
$legacyHubUrl = if ($existing.ContainsKey('HUB_URL')) { $existing['HUB_URL'] } elseif ($legacy.ContainsKey('HUB_URL')) { $legacy['HUB_URL'] } else { $null }
$effectiveHubUrl = if ($HubUrl) { $HubUrl.TrimEnd('/') } elseif ($legacyHubUrl) { $legacyHubUrl.TrimEnd('/') } else { $null }

if (-not $runtimeToken) { throw 'Existing runtime credential is required; refusing to generate or rotate it.' }
if (-not $effectiveHubUrl) { throw 'Existing HUB_URL or explicit -HubUrl is required; refusing to guess it.' }
if (-not (Test-Path -LiteralPath $HubPublicKeyFile)) { throw 'Existing Hub public key is required.' }

if ($DryRun) {
    [ordered]@{
        status = 'dry_run'
        canonical_task = $TaskName
        principal = 'SYSTEM'
        trigger = 'AtStartup'
        install_dir = $InstallDir
        expected_build = $ExpectedBuild
        legacy_tasks = $LegacyTaskNames
        unrelated_task_untouched = $LegacyUnrelatedTask
        credential = 'REDACTED'
    } | ConvertTo-Json -Depth 5
    exit 0
}

if (-not (Test-Administrator)) { throw 'Administrator privileges are required for SYSTEM AtStartup installation.' }

$stamp = Get-Date -Format 'yyyyMMddTHHmmss'
$ReceiptDir = Join-Path $ReceiptRoot $stamp
$BackupDir = Join-Path $ReceiptDir 'files'
New-Item -ItemType Directory -Force -Path $ReceiptDir | Out-Null
Set-PrivatePathAcl -Path $ReceiptDir
New-Item -ItemType Directory -Force -Path $BackupDir | Out-Null
Set-PrivatePathAcl -Path $BackupDir

$backups = @()
foreach ($path in @($CurrentExe, $EnvFile, $RunScript)) {
    $backups += Copy-Backup -Path $path -BackupDirectory $BackupDir
}

$legacyReceipts = @()
foreach ($name in $LegacyTaskNames) {
    $legacyReceipts += Get-TaskReceipt -Name $name -Directory $ReceiptDir
}
$canonicalBefore = Get-TaskReceipt -Name $TaskName -Directory $ReceiptDir
$credentialSha256 = Get-StringSha256 -Value $runtimeToken
$credentialDigestMatch = $false
$receipt = [ordered]@{
    schema = 'gptadmin.windows-shellmcp-autostart/v2'
    created_at = (Get-Date).ToString('o')
    status = 'pre_mutation'
    receipt_dir = $ReceiptDir
    canonical_task = $TaskName
    canonical_before = $canonicalBefore
    canonical_principal = 'SYSTEM'
    canonical_trigger = 'AtStartup'
    install_dir = $InstallDir
    installed_exe = $CurrentExe
    expected_build = $ExpectedBuild
    package_sha256 = $ExpectedSha256.ToLowerInvariant()
    installed_exe_sha256 = $null
    backups = $backups
    legacy_tasks = $legacyReceipts
    fallback_launcher = $FallbackLauncherPath
    unrelated_task_untouched = $LegacyUnrelatedTask
    credential = 'REDACTED'
    credential_sha256 = $credentialSha256
    credential_digest_match = $false
}
$ReceiptPath = Join-Path $ReceiptDir 'receipt.json'
$receipt | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ReceiptPath -Encoding UTF8
Set-PrivatePathAcl -Path $ReceiptPath

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("gptadmin-autostart-$stamp")
try {
    New-Item -ItemType Directory -Force -Path $BinDir, $LogDir | Out-Null
    Set-PrivatePathAcl -Path $LogDir
    New-Item -ItemType Directory -Path $tmp | Out-Null
    $archive = Join-Path $tmp 'gptadmin-win.zip'
    Invoke-WebRequest -UseBasicParsing -Uri $PackageUrl -OutFile $archive
    $archiveHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
    if ($archiveHash -ne $ExpectedSha256.ToLowerInvariant()) {
        throw "public archive digest mismatch: $archiveHash"
    }
    Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force
    $candidate = Get-ChildItem -LiteralPath $tmp -Recurse -File -Filter 'shellmcp.exe' | Select-Object -First 1
    if (-not $candidate) { throw 'shellmcp.exe missing from public archive' }
    Copy-Item -LiteralPath $candidate.FullName -Destination $CurrentExe -Force

    @(
        "SHELLMCP_TOKEN=$runtimeToken",
        "HUB_URL=$effectiveHubUrl",
        'SHELLMCP_PORT=25900',
        'SHELLMCP_HOST=127.0.0.1',
        'SHELLMCP_TRANSPORT=polling',
        'SHELLMCP_MODE=long_poll',
        'SHELLMCP_QUEUE=1',
        'SHELLMCP_HEARTBEAT=0',
        'SHELLMCP_NAME=BeyondInfinity',
        'SHELLMCP_URL=http://127.0.0.1:25900',
        "SHELLMCP_IDENTITY_DIR=$InstallDir",
        "SHELLMCP_SPOOL_DIR=$(Join-Path $InstallDir 'spool')",
        "SHELLMCP_OUTBOX_DIR=$(Join-Path $InstallDir 'spool\outbox')",
        "HUB_PUBLIC_KEY_FILE=$HubPublicKeyFile",
        'SHELLMCP_SERVICE_NAME=gptadmin-shellmcp',
        'SHELLMCP_SERVICE_SCOPE=system',
        'SHELLMCP_AUTO_UPDATE=1',
        'SHELLMCP_UPDATE_INTERVAL_S=3600',
        "SHELLMCP_UPDATE_MANIFEST_URL=$effectiveHubUrl/artifacts/shellmcp.json",
        "SHELLMCP_UPDATE_TOKEN=$runtimeToken"
    ) | Set-Content -LiteralPath $EnvFile -Encoding ASCII
    Set-SecretFileAcl -Path $EnvFile
    $persisted = Read-EnvMap -Path $EnvFile
    $credentialDigestMatch = $persisted.ContainsKey('SHELLMCP_TOKEN') -and ((Get-StringSha256 -Value $persisted['SHELLMCP_TOKEN']) -eq $credentialSha256)
    if (-not $credentialDigestMatch) { throw 'Persisted credential digest does not match stdin/source credential.' }

    @'
$ErrorActionPreference = 'Stop'
$installDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$envFile = Join-Path $installDir 'shellmcp.env'
$bootstrap = Join-Path $installDir 'logs\shellmcp.bootstrap.log'
$stdout = Join-Path $installDir 'logs\shellmcp.stdout.log'
$stderr = Join-Path $installDir 'logs\shellmcp.stderr.log'
try {
    Add-Content -LiteralPath $bootstrap -Value "$(Get-Date -Format o) launcher_start"
    foreach ($line in Get-Content -LiteralPath $envFile) {
        if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
        $parts = $line -split '=', 2
        [Environment]::SetEnvironmentVariable($parts[0], $parts[1], 'Process')
    }
    Add-Content -LiteralPath $bootstrap -Value "$(Get-Date -Format o) env_loaded"
    $exe = Join-Path $installDir 'bin\shellmcp.exe'
    $process = Start-Process -FilePath $exe -PassThru -Wait -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    $code = $process.ExitCode
    Add-Content -LiteralPath $bootstrap -Value "$(Get-Date -Format o) process_exit=$code"
    exit $code
} catch {
    $message = [string]$_.Exception.Message
    $message = [regex]::Replace($message, '(?i)(token|secret|password|key)=\S+', '$1=REDACTED')
    Add-Content -LiteralPath $bootstrap -Value "$(Get-Date -Format o) bootstrap_error=$message"
    exit 1
}
'@ | Set-Content -LiteralPath $RunScript -Encoding UTF8

    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$RunScript`""
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null

    foreach ($name in $LegacyTaskNames) {
        if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) {
            Stop-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
            Disable-ScheduledTask -TaskName $name | Out-Null
        }
    }

    $receipt.status = 'installed_no_start'
    $receipt.installed_exe_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $CurrentExe).Hash.ToLowerInvariant()
    $receipt.credential_digest_match = $credentialDigestMatch
    $receipt | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ReceiptPath -Encoding UTF8
    if (-not $NoStart) {
        Start-ScheduledTask -TaskName $TaskName
        $receipt.status = 'started'
        $receipt | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ReceiptPath -Encoding UTF8
    }
} catch {
    $message = [regex]::Replace([string]$_.Exception.Message, '(?i)(token|secret|password|key)=\S+', '$1=REDACTED')
    $receipt.status = 'mutation_failed'
    $receipt.failure = $message
    try { $receipt | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ReceiptPath -Encoding UTF8 } catch { }
    Invoke-Rollback -ReceiptPath $ReceiptPath | Out-Null
    throw "installation failed; automatic rollback completed: $message"
} finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

[ordered]@{
    status = $receipt.status
    receipt = $ReceiptPath
    canonical_task = $TaskName
    expected_build = $ExpectedBuild
    package_sha256 = $ExpectedSha256.ToLowerInvariant()
    credential = 'REDACTED'
    credential_digest_match = $credentialDigestMatch
    unrelated_task_untouched = $LegacyUnrelatedTask
} | ConvertTo-Json -Depth 5
