#requires -version 5.1
<#
GPT Admin Windows installer.

Default (no -HubUrl): installs one GPTAdmin Node with an embedded executor.
Use -Role node for a full local installation. Use -Role executor -HubUrl URL
to run only ShellMCP against an existing Hub. Providing a remote -HubUrl
continues to select executor mode. Repeat installs retain the existing role,
device identity, and Hub credentials.

Remote agent mode (explicit non-loopback -HubUrl): installs only the
ShellMCP agent against an existing Hub. The agent generates its own
credential, registers with that Hub, and waits in the Hub pending list
until that Hub's admin approves server "shell:<COMPUTERNAME>".

No git checkout, no private repo, no local Go or Python runtime.
Autostart uses built-in Windows Task Scheduler.

For a fresh side-by-side remote agent, pass -Isolated -System with explicit
-InstallDir, -HubUrl, -TaskName, -ShellmcpPort, -ShellmcpName and -PackageUrl.
It generates separate credentials and state, refuses existing targets, and
supports -NoStart. Self-repair is disabled by default in isolated mode. Enable
it only after verifying the installed candidate is build 1156 or newer, with
the task-isolated Windows updater; older public packages may target Blue.
-LatestBuild pins the normal GitHub update target; -DisableSelfRepair stages
the installation with automatic self-repair disabled.

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
    [ValidateSet('node', 'executor')]
    [string]$Role = $env:GPTADMIN_INSTALL_ROLE,
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
    [switch]$NoStart,
    [switch]$Isolated,
    [ValidateRange(0, 2147483647)]
    [int]$LatestBuild = 0,
    [switch]$DisableSelfRepair
)

$ErrorActionPreference = 'Stop'
$InstallerArguments = $PSBoundParameters

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

function Stop-OwnedRuntime([string]$Name, [string]$Path) {
    foreach ($owned in @(Get-OwnedProcess -Name $Name -Path $Path)) {
        $owned.Kill()
        if (-not $owned.WaitForExit(10000)) { throw "$Name did not stop at $Path." }
        $owned.Dispose()
    }
}

$IsAdmin = Test-Admin
$UserMode = $User -or ((-not $System) -and (-not $IsAdmin))

# Isolated is an initial side-by-side installation, never an upgrade/uninstall.
# Check supplied arguments before creating files, changing tasks or downloading.
if ($Isolated) {
    if (-not $System -or $User -or $Uninstall) {
        throw 'Isolated install requires -System and cannot use -User or -Uninstall.'
    }
    foreach ($required in @('InstallDir', 'HubUrl', 'TaskName', 'ShellmcpPort', 'ShellmcpName', 'PackageUrl')) {
        if (-not $InstallerArguments.ContainsKey($required)) {
            throw "Isolated install requires an explicit -$required."
        }
    }
    if ([string]::IsNullOrWhiteSpace($PackageUrl)) {
        throw 'Isolated install requires a nonempty -PackageUrl.'
    }
    if (-not $InstallerArguments.ContainsKey('DisableSelfRepair')) { $DisableSelfRepair = $true }
    if ($InstallerArguments.ContainsKey('ShellmcpToken')) {
        throw 'Isolated install generates a new local credential; do not supply -ShellmcpToken.'
    }
    try { $isolatedHub = [Uri]$HubUrl } catch { throw 'Isolated install requires an absolute remote Hub URL.' }
    if (-not $isolatedHub.IsAbsoluteUri -or $isolatedHub.Scheme -notin @('http', 'https') -or $isolatedHub.IsLoopback) {
        throw 'Isolated install requires an absolute remote Hub URL.'
    }
    if (-not $InstallDir -or -not [IO.Path]::IsPathRooted($InstallDir)) {
        throw 'Isolated install requires an absolute installation directory.'
    }
    $InstallDir = [IO.Path]::GetFullPath($InstallDir).TrimEnd('\')
    $reservedDirs = @((Join-Path $env:ProgramData 'gptadmin'), (Join-Path $env:LOCALAPPDATA 'gptadmin'))
    if ($env:GPTADMIN_DIR) { $reservedDirs += $env:GPTADMIN_DIR }
    foreach ($reserved in $reservedDirs) {
        $reservedPath = [IO.Path]::GetFullPath($reserved).TrimEnd('\')
        if ($InstallDir.Equals($reservedPath, [StringComparison]::OrdinalIgnoreCase) -or
            $InstallDir.StartsWith($reservedPath + '\', [StringComparison]::OrdinalIgnoreCase) -or
            $reservedPath.StartsWith($InstallDir + '\', [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Isolated install directory must be separate from the existing/default installation.'
        }
    }
    if (-not $TaskName -or $TaskName.Length -gt 180 -or $TaskName -match '[\\/:*?"<>|\x00-\x1f]' -or
        $TaskName -in @('.', '..') -or $TaskName -eq 'gptadmin-shellmcp' -or $TaskName -eq $env:SHELLMCP_SERVICE_NAME -or $TaskName -eq $env:SHELLMCP_TASK_NAME) {
        throw 'Isolated install requires a distinct non-default task name.'
    }
    if ($ShellmcpPort -lt 1 -or $ShellmcpPort -gt 65535 -or $ShellmcpPort -eq 25900) {
        throw 'Isolated install requires a distinct non-default port in 1..65535.'
    }
    foreach ($oldPort in @($env:SHELL_PORT, $env:SHELLMCP_PORT)) {
        if ($oldPort -and [string]$ShellmcpPort -eq [string]$oldPort) {
            throw 'Isolated install port matches the inherited agent port.'
        }
    }
    if (-not $ShellmcpName -or $ShellmcpName -eq $env:COMPUTERNAME -or
        $ShellmcpName -eq $env:SHELL_NAME -or $ShellmcpName -eq $env:SHELLMCP_NAME) {
        throw 'Isolated install requires a distinct agent name.'
    }
    $HubUrl = $HubUrl.TrimEnd('/')
    $ShellmcpToken = ''
    if (-not $InstallerArguments.ContainsKey('ShellmcpBind')) { $ShellmcpBind = '127.0.0.1' }
    if (-not $InstallerArguments.ContainsKey('ShellmcpTransport')) { $ShellmcpTransport = 'polling' }
    if (-not $InstallerArguments.ContainsKey('ShellmcpUrl')) { $ShellmcpUrl = "http://$env:COMPUTERNAME" + ':' + $ShellmcpPort }
    if (-not $InstallerArguments.ContainsKey('HubPublicKey')) { $HubPublicKey = 'mEhYDiOc9ZxY54dXelDTsVD2Wjew3f6R0f2dVW2qKPQ' }
}

if (-not $InstallDir) {
    if ($UserMode) { $InstallDir = Join-Path $env:LOCALAPPDATA 'gptadmin' }
    else { $InstallDir = Join-Path $env:ProgramData 'gptadmin' }
}

function Read-InstallEnv {
    param([string]$Path)
    $values = @{}
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        foreach ($line in Get-Content -LiteralPath $Path) {
            $kv = $line -split '=', 2
            if ($kv.Length -eq 2) { $values[$kv[0]] = $kv[1] }
        }
    }
    return $values
}

$previousNode = Read-InstallEnv (Join-Path $InstallDir 'node.env')
$previousHub = Read-InstallEnv (Join-Path $InstallDir 'hub.env')
$previousAgent = Read-InstallEnv (Join-Path $InstallDir 'shellmcp.env')
$installedRole = if ($previousNode.Count -or $previousHub.Count) { 'node' }
                 elseif ($previousAgent.Count) { 'executor' }
if ($Isolated -and $installedRole) { throw 'Isolated install refuses an existing runtime configuration.' }
if (-not $Role) {
    if ($HubUrl) { $Role = if (Test-LoopbackUrl $HubUrl) { 'node' } else { 'executor' } }
    elseif ($installedRole) { $Role = $installedRole }
    else { $Role = 'node' }
}
$LocalHub = $Role -eq 'node'
if ($installedRole -and $Role -ne $installedRole -and -not $Uninstall) {
    throw "Installation directory already holds role '$installedRole'. Use a separate directory for role '$Role'."
}
if ($Isolated -and $LocalHub) { throw 'The -Isolated option installs an executor.' }
if ($LocalHub) {
    if ($HubUrl -and -not (Test-LoopbackUrl $HubUrl)) { throw 'A node starts its own Hub. Use -Role executor to connect to a remote Hub.' }
    $saved = if ($previousNode.Count) { $previousNode } else { @{} + $previousAgent }
    if (-not $previousNode.Count) { foreach ($key in $previousHub.Keys) { $saved[$key] = $previousHub[$key] } }
    if (-not $InstallerArguments.ContainsKey('HubPort') -and -not $env:GPTADMIN_HUB_PORT -and $saved.GPTADMIN_HUB_PORT) { $HubPort = [int]$saved.GPTADMIN_HUB_PORT }
    $HubUrl = "http://127.0.0.1:$HubPort"
    if (-not $CtlToken) { $CtlToken = $saved.CTL_TOKEN }
    if (-not $AdminPassword) { $AdminPassword = $saved.ADMIN_PASSWORD }
    if (-not $OAuthClientSecret) { $OAuthClientSecret = $saved.OAUTH_CLIENT_SECRET }
} else {
    $saved = $previousAgent
    if (-not $HubUrl) { $HubUrl = $saved.HUB_URL }
    if (-not $HubUrl -and -not $Uninstall) { throw 'An executor needs -HubUrl pointing to its managing Hub.' }
}
if (-not $ShellmcpToken) { $ShellmcpToken = $saved.SHELLMCP_TOKEN }
if (-not $InstallerArguments.ContainsKey('ShellmcpPort') -and -not $env:SHELLMCP_PORT -and $saved.SHELLMCP_PORT) { $ShellmcpPort = [int]$saved.SHELLMCP_PORT }
if (-not $InstallerArguments.ContainsKey('ShellmcpTransport') -and -not $env:SHELLMCP_TRANSPORT -and $saved.SHELLMCP_TRANSPORT) { $ShellmcpTransport = $saved.SHELLMCP_TRANSPORT }
if (-not $InstallerArguments.ContainsKey('ShellmcpBind') -and -not $env:SHELLMCP_BIND -and $saved.SHELLMCP_HOST) { $ShellmcpBind = $saved.SHELLMCP_HOST }
if (-not $ShellmcpUrl) { $ShellmcpUrl = $saved.SHELLMCP_URL }
if (-not $ShellmcpName) { $ShellmcpName = $saved.SHELLMCP_NAME }
if (-not $ShellmcpName) { $ShellmcpName = $previousAgent.SHELLMCP_NAME }
if (-not $TaskName -and ($previousNode.Count -or -not $LocalHub)) { $TaskName = $saved.SHELLMCP_SERVICE_NAME }

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
$runtimeName = if ($LocalHub) { 'gptadmin-node' } else { 'shellmcp' }
if (-not $TaskName) {
    $TaskName = if ($LocalHub) { 'gptadmin-node' } else { 'gptadmin-shellmcp' }
    if ($UserMode) { $TaskName += "-$env:USERNAME" }
}

if ($TaskName -match '[\\/:*?"<>|\x00-\x1f]' -or $TaskName -in @('.', '..')) { throw 'Invalid Windows task name.' }
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$BinDir = Join-Path $InstallDir 'bin'
$LogDir = Join-Path $InstallDir 'logs'
$IdentityDir = Join-Path $InstallDir 'identity'
$SpoolDir = Join-Path $InstallDir 'spool'
$OutboxDir = Join-Path $SpoolDir 'outbox'
$ConfigDir = Join-Path $InstallDir 'config'
$McpConfigFile = Join-Path $ConfigDir 'mcp.json'
$PublicDir = Join-Path $InstallDir 'public'
$runtimeBase = if ($LocalHub) { 'node' } else { 'shellmcp' }
$EnvFile = Join-Path $InstallDir "$runtimeBase.env"
$RunScript = Join-Path $InstallDir "run_$runtimeBase.ps1"
$BatchScript = Join-Path $InstallDir "run_$runtimeBase.cmd"
$CredentialsFile = Join-Path $InstallDir 'ADMIN-CREDENTIALS.txt'
$HubPublicKeyFile = Join-Path $InstallDir 'hub_ed25519.pub'
$CurrentExe = Join-Path $BinDir "$runtimeName.exe"
$StartupDir = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Startup'
$startupName = if ($LocalHub) { 'gptadmin-node' } else { 'gptadmin-shellmcp' }
if ($TaskName -ne "$startupName-$env:USERNAME" -and $TaskName -ne $startupName) { $startupName = $TaskName }
$StartupScript = Join-Path $StartupDir "$startupName.cmd"

function Require-Admin {
    if (-not $IsAdmin) {
        throw 'Run PowerShell as Administrator, or use -User for per-user install.'
    }
}

function Assert-IsolatedTarget {
    if (-not $Isolated) { return }
    if (Test-Path -LiteralPath $BinDir) {
        throw 'Isolated install refuses an existing bin directory.'
    }
    if (Test-Path -LiteralPath $CurrentExe) {
        throw 'Isolated install refuses an existing executable.'
    }
    $existingTask = @(Get-ScheduledTask -TaskPath '\' -ErrorAction Stop | Where-Object { $_.TaskName -eq $TaskName })
    if ($existingTask.Count -gt 0) {
        throw 'Isolated install refuses an existing scheduled task.'
    }
}

function Get-WindowsPackageArch {
    # PROCESSOR_ARCHITEW6432 identifies the native OS when PowerShell runs under WOW64.
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) {
        try { $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() }
        catch { $arch = $env:PROCESSOR_ARCHITECTURE }
    }
    switch ($arch) {
        'X64' { return 'x64' }
        'AMD64' { return 'x64' }
        'ARM64' { return 'arm64' }
        default { throw "Unsupported Windows OS architecture '$arch'." }
    }
}

function Get-WindowsReleaseText([string]$Uri) {
    $headers = @{ 'User-Agent' = 'GPTAdmin-Windows-Installer' }
    $response = Invoke-WebRequest -UseBasicParsing -Uri $Uri -Headers $headers -TimeoutSec 30
    if ($response.Content -is [byte[]]) { return [Text.Encoding]::UTF8.GetString($response.Content) }
    return [string]$response.Content
}

function Get-WindowsReleaseCandidates {
    $repo = 'megamen32/gptadmin_opensource'
    $candidates = @()
    try {
        for ($page = 1; ; $page++) {
            $api = "https://api.github.com/repos/$repo/releases?per_page=100&page=$page"
            $items = @(ConvertFrom-Json -InputObject (Get-WindowsReleaseText $api))
            if ($items.Count -eq 1 -and $items[0] -is [array]) { $items = $items[0] }
            foreach ($release in $items) {
                if (!$release.draft -and !$release.prerelease -and $release.tag_name -cmatch '^v(0|[1-9][0-9]*)$') {
                    $number = [long]$Matches[1]
                    if (@($release.assets | Where-Object { $_.name -ceq 'gptadmin-release-matrix.json' }).Count) {
                        $candidates += [pscustomobject]@{ Tag=$release.tag_name; Version=$number; FromFeed=$false }
                    }
                }
            }
            if ($items.Count -lt 100) { break }
        }
    } catch {
        # The anonymous API has a low rate limit. The public release feed uses
        # github.com without an API token. Trust only exact official release links.
        $candidates = @()
        $raw = Get-WindowsReleaseText "https://github.com/$repo/releases.atom"
        $feed = New-Object System.Xml.XmlDocument
        $feed.XmlResolver = $null
        $feed.LoadXml($raw)
        $namespaces = New-Object System.Xml.XmlNamespaceManager($feed.NameTable)
        $namespaces.AddNamespace('a','http://www.w3.org/2005/Atom')
        $pattern = '^https://github\.com/megamen32/gptadmin_opensource/releases/tag/(v(0|[1-9][0-9]*))$'
        foreach ($link in $feed.SelectNodes('/a:feed/a:entry/a:link[@rel="alternate"]', $namespaces)) {
            if ($link.GetAttribute('href') -cmatch $pattern) {
                $candidates += [pscustomobject]@{ Tag=$Matches[1]; Version=[long]$Matches[2]; FromFeed=$true }
            }
        }
    }
    return @($candidates | Sort-Object -Property Version -Descending -Unique)
}

function Resolve-WindowsReleasePackage([string]$Edition, [string]$Arch) {
    $origin = 'https://github.com/megamen32/gptadmin_opensource/releases'
    $name = "gptadmin-windows-$Arch-$Edition.zip"
    foreach ($release in @(Get-WindowsReleaseCandidates)) {
        $download = "$origin/download/$($release.Tag)"
        if ($release.FromFeed) {
            # Drafts are private. Numeric prereleases can appear in the public feed.
            $page = Get-WindowsReleaseText "$origin/tag/$($release.Tag)"
            if ($page -match '>\s*Pre-release\s*<') { continue }
        }
        try {
            $matrix = ConvertFrom-Json -InputObject (Get-WindowsReleaseText "$download/gptadmin-release-matrix.json")
        } catch {
            if ($release.FromFeed -and [int]$_.Exception.Response.StatusCode -eq 404) { continue }
            throw
        }
        if ($matrix.schema -cne 'gptadmin.release-matrix/v2' -or [string]$matrix.build_version -cne [string]$release.Version) {
            throw "Release matrix does not match $($release.Tag)."
        }
        $entries = @($matrix.artifacts | Where-Object {
            $_.platform -ceq 'windows' -and $_.arch -ceq $Arch -and $_.edition -ceq $Edition
        })
        if ($entries.Count -ne 1 -or $entries[0].file -cne $name -or [string]$entries[0].sha256 -cnotmatch '^[0-9a-f]{64}$' -or [long]$entries[0].size -le 0) {
            throw "Release $($release.Tag) must contain one verified $name."
        }
        return [pscustomobject]@{ Url="$download/$name"; Tag=$release.Tag; Size=[long]$entries[0].size; Sha256=$entries[0].sha256 }
    }
    throw 'No stable numeric GPTAdmin release with a release matrix is available.'
}

function Assert-WindowsPackage([string]$Path, $Expected) {
    if ((Get-Item -LiteralPath $Path).Length -ne $Expected.Size -or
        (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ne $Expected.Sha256) {
        throw "Windows package size or SHA-256 differs from release $($Expected.Tag)."
    }
}

function Download-And-InstallArtifact {
    $expectedPackage = $null
    if (-not $PackageUrl) {
        $edition = if ($LocalHub) { 'full' } else { 'client' }
        $expectedPackage = Resolve-WindowsReleasePackage $edition (Get-WindowsPackageArch)
        $PackageUrl = $expectedPackage.Url
    }
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('gptadmin-win-' + [System.Guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    $archive = Join-Path $tmp 'gptadmin-win.zip'

    try {
        if (Test-Path -LiteralPath $PackageUrl -ErrorAction SilentlyContinue) {
            Write-Host "Installing local package $PackageUrl"
            Copy-Item -LiteralPath $PackageUrl -Destination $archive -Force
        } else {
            Write-Host "Downloading $PackageUrl"
            Invoke-WebRequest -UseBasicParsing -Uri $PackageUrl -OutFile $archive
        }
        if ($expectedPackage) { Assert-WindowsPackage $archive $expectedPackage }
        Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force

        $names = if ($LocalHub) { @('gptadmin-node.exe') } else { @('shellmcp.exe', 'shellmcp_win.exe') }
        $exe = Get-ChildItem -LiteralPath $tmp -Recurse -File | Where-Object { $_.Name -in $names } | Select-Object -First 1
        if (-not $exe) { throw "Package is missing the $runtimeName executable required by role $Role." }

        New-Item -ItemType Directory -Force -Path $InstallDir,$BinDir,$LogDir,$IdentityDir,$ConfigDir | Out-Null
        if ($UserMode) {
            # User installs must not depend on Task Scheduler access.
            Stop-OwnedRuntime -Name $runtimeName -Path $CurrentExe
        } else {
            $existing = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
            if ($existing) {
                if (-not ([string]$existing.Actions.Arguments).Contains(('"{0}"' -f $RunScript))) { throw "Task $TaskName belongs to another installation." }
                Stop-ScheduledTask -TaskName $TaskName -ErrorAction Stop
                Stop-OwnedRuntime -Name $runtimeName -Path $CurrentExe
            }
        }
        if (Test-Path -LiteralPath $CurrentExe) { Copy-Item -LiteralPath $CurrentExe -Destination "$CurrentExe.bak" -Force }
        Copy-Item -LiteralPath $exe.FullName -Destination $CurrentExe -Force
        if ($LocalHub) {
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
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Write-Config {
    Set-Content -Path $HubPublicKeyFile -Value $HubPublicKey -Encoding ASCII
    $queueEnabled = if ($ShellmcpTransport -eq 'polling') { '1' } else { '0' }
    $agentEnv = @(
        "SHELLMCP_TOKEN=$ShellmcpToken",
        "SHELL_TOKEN=$ShellmcpToken",
        "HUB_URL=$HubUrl",
        "SHELLMCP_PORT=$ShellmcpPort",
        "SHELLMCP_HOST=$ShellmcpBind",
        "SHELLMCP_TRANSPORT=$ShellmcpTransport",
        "SHELLMCP_URL=$ShellmcpUrl",
        "SHELLMCP_NAME=$ShellmcpName",
        "SHELL_NAME=$ShellmcpName",
        "SHELLMCP_IDENTITY_DIR=$IdentityDir",
        "SHELL_IDENTITY_DIR=$IdentityDir",
        "SHELL_SPOOL_DIR=$SpoolDir",
        "SHELLMCP_SPOOL_DIR=$SpoolDir",
        "SHELL_OUTBOX_DIR=$OutboxDir",
        "SHELLMCP_OUTBOX_DIR=$OutboxDir",
        "SHELLMCP_QUEUE=$queueEnabled",
        "HUB_PUBLIC_KEY_FILE=$HubPublicKeyFile",
        "SHELLMCP_SERVICE_NAME=$TaskName",
        "SHELLMCP_SERVICE_SCOPE=$(if ($UserMode) { 'user' } else { 'system' })",
        "SHELLMCP_AUTO_UPDATE=$(if ($LocalHub) { '0' } elseif ($saved.ContainsKey('SHELLMCP_AUTO_UPDATE')) { $saved.SHELLMCP_AUTO_UPDATE } else { '1' })",
        "SHELLMCP_UPDATE_INTERVAL_S=3600",
        "SHELLMCP_UPDATE_MANIFEST_URL=$HubUrl/artifacts/shellmcp.json",
        "SHELLMCP_UPDATE_TOKEN=$ShellmcpToken"
    )
    if ($Isolated) {
        $mode = if ($queueEnabled -eq '1') { 'long_poll' } else { 'webhook' }
        $agentEnv += @(
            "SHELL_TOKEN=$ShellmcpToken",
            "SHELL_PORT=$ShellmcpPort",
            "SHELL_HOST=$ShellmcpBind",
            "SHELL_URL=$ShellmcpUrl",
            "SHELL_QUEUE=$queueEnabled",
            "SHELL_MODE=$mode",
            "SHELLMCP_MODE=$mode",
            "QUEUE_URL=$HubUrl/queue",
            "SHELLMCP_QUEUE_URL=$HubUrl/queue",
            "HUB_PUBLIC_KEY=$HubPublicKey",
            "SHELLMCP_MCP_CONFIG=$McpConfigFile",
            "GPTADMIN_MCP_CONFIG=$McpConfigFile",
            "GPTADMIN_MCP_AGENTS_DIR=$McpConfigFile",
            "SHELLMCP_AUDIT_LOG=$LogDir\execution.jsonl",
            "SHELLMCP_LEGACY_MANIFEST_UPDATE=0"
        )
        Set-Content -LiteralPath $McpConfigFile -Value '[]' -Encoding ASCII
    }
    if ($Isolated -or $InstallerArguments.ContainsKey('LatestBuild')) {
        $agentEnv += "SHELLMCP_LATEST_BUILD=$(if ($LatestBuild -gt 0) { $LatestBuild } else { '' })"
    }
    if ($Isolated -or $InstallerArguments.ContainsKey('DisableSelfRepair')) {
        $agentEnv += "SHELLMCP_SELF_REPAIR_DISABLE=$(if ($DisableSelfRepair) { '1' } else { '0' })"
    }
    if ($LocalHub) {
        $agentEnv += @(
            "GPTADMIN_ROOT=$InstallDir",
            "GPTADMIN_CONFIG_DIR=$ConfigDir",
            "GPTADMIN_PUBLIC_DIR=$PublicDir",
            "GPTADMIN_HUB_HOST=127.0.0.1",
            "GPTADMIN_HUB_PORT=$HubPort",
            "PUBLIC_ORIGIN=$HubUrl",
            "GPTADMIN_STANDALONE=1",
            "CTL_TOKEN=$CtlToken",
            "ADMIN_PASSWORD=$AdminPassword",
            "OAUTH_CLIENT_SECRET=$OAuthClientSecret"
        )
        @("GPTAdmin Node", "Admin URL: $HubUrl/admin", "Connect: $HubUrl/connect", "AdminPassword: $AdminPassword") |
            Set-Content -Path $CredentialsFile -Encoding UTF8
    }
    # Preserve explicit peer, profile and transport options set by the operator.
    $merged = @{} + $saved
    foreach ($line in $agentEnv) {
        $kv = $line -split '=', 2
        $merged[$kv[0]] = $kv[1]
    }
    if (-not $LocalHub) {
        foreach ($key in @('ADMIN_PASSWORD', 'CTL_TOKEN', 'OAUTH_CLIENT_SECRET', 'MCP_RELAY_ENROLLMENT_PASSWORD', 'GPTADMIN_MCP_RELAY_ENROLLMENT_PASSWORD')) { $merged.Remove($key) }
    }
    $merged.GetEnumerator() | Sort-Object Key | ForEach-Object { "$($_.Key)=$($_.Value)" } |
        Set-Content -LiteralPath $EnvFile -Encoding UTF8

    @"
@echo off
if /i "%~1"=="--runtime" goto runtime
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0run_$runtimeBase.ps1"
exit /b %errorlevel%
:runtime
"%~dp0bin\$runtimeName.exe" >> "%~dp0logs\$runtimeBase.task.log" 2>&1
exit /b %errorlevel%
"@ | Set-Content -Path $BatchScript -Encoding ASCII

    @'
$ErrorActionPreference = 'Stop'
$base = ([IO.Path]::GetFileNameWithoutExtension($PSCommandPath)).Substring(4)
Get-Content -LiteralPath (Join-Path $PSScriptRoot "$base.env") | ForEach-Object {
    $parts = $_ -split '=', 2
    if ($parts.Length -eq 2 -and $parts[0]) {
        if ($parts[1].Length) { Set-Item -LiteralPath ("Env:" + $parts[0]) -Value $parts[1] }
        else { Remove-Item -LiteralPath ("Env:" + $parts[0]) -ErrorAction SilentlyContinue }
    }
}
if ($base -eq 'shellmcp') {
    foreach ($key in @('ADMIN_PASSWORD', 'CTL_TOKEN', 'OAUTH_CLIENT_SECRET', 'MCP_RELAY_ENROLLMENT_PASSWORD', 'GPTADMIN_MCP_RELAY_ENROLLMENT_PASSWORD')) {
        Remove-Item -LiteralPath ("Env:" + $key) -ErrorAction SilentlyContinue
    }
}
# Windows PowerShell 5.1 treats native stderr as a terminating error under Stop.
# Let cmd.exe append the two native streams, including normal startup diagnostics.
& (Join-Path $PSScriptRoot "run_$base.cmd") '--runtime'
exit $LASTEXITCODE
'@ | Set-Content -Path $RunScript -Encoding UTF8
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
    if ($UserMode -and (Test-Path -LiteralPath $Startup) -and -not (Get-Content -LiteralPath $Startup -Raw).Contains($Batch)) {
        throw "Startup launcher $Startup belongs to another installation."
    }
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

function Get-LegacyLaunchers {
    if (-not $LocalHub -or -not $previousHub.Count) { return }
    $oldShellName = $previousAgent.SHELLMCP_SERVICE_NAME
    if (-not $oldShellName) { $oldShellName = 'gptadmin-shellmcp' + $(if ($UserMode) { "-$env:USERNAME" }) }
    foreach ($base in @('shellmcp', 'hub')) {
        $oldRun = Join-Path $InstallDir "run_$base.ps1"
        $oldBatch = Join-Path $InstallDir "run_$base.cmd"
        $oldTask = if ($base -eq 'hub') { $oldShellName -replace '^gptadmin-shellmcp','gptadmin-hub' } else { $oldShellName }
        $oldStartup = Join-Path $StartupDir "gptadmin-$base.cmd"
        if ($UserMode) {
            if ((Test-Path -LiteralPath $oldStartup) -and (Get-Content -Raw -LiteralPath $oldStartup).Contains($oldBatch)) {
                [pscustomobject]@{ Name=$oldTask; Path=$oldStartup; Batch=$oldBatch; User=$true }
            }
        } else {
            $task = Get-ScheduledTask -TaskName $oldTask -ErrorAction SilentlyContinue
            if ($task -and ([string]$task.Actions.Arguments).Contains(('"{0}"' -f $oldRun))) {
                [pscustomobject]@{ Name=$oldTask; Path=$oldRun; User=$false }
            }
        }
    }
}

if (-not $UserMode) { Require-Admin }

if ($Uninstall) {
    if ($UserMode) {
        if ((Test-Path -LiteralPath $StartupScript) -and (Get-Content -Raw -LiteralPath $StartupScript).Contains($BatchScript)) {
            Remove-Item -LiteralPath $StartupScript -Force
        }
    } else {
        $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        if ($task -and ([string]$task.Actions.Arguments).Contains(('"{0}"' -f $RunScript))) {
            Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction Stop
        }
    }
    Stop-OwnedRuntime -Name $runtimeName -Path $CurrentExe
    exit 0
}

Assert-IsolatedTarget
Download-And-InstallArtifact
Write-Config

$legacyLaunchers = @(Get-LegacyLaunchers)
foreach ($old in $legacyLaunchers) {
    if (-not $old.User) { Stop-ScheduledTask -TaskName $old.Name -ErrorAction Stop }
}
if ($legacyLaunchers.Count) {
    foreach ($oldName in @('shellmcp', 'gptadmin-hub')) {
        Stop-OwnedRuntime -Name $oldName -Path (Join-Path $BinDir "$oldName.exe")
    }
}
try {
    $backend = Install-Task -Name $TaskName -Run $RunScript -Batch $BatchScript -Startup $StartupScript
    if (-not $NoStart) {
        if ($backend -eq 'task') { Start-ScheduledTask -TaskName $TaskName }
        else { Start-Process -FilePath 'powershell.exe' -WindowStyle Hidden -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', ('"{0}"' -f $RunScript)) }
        if ($LocalHub -and -not (Wait-HubHealth)) { throw "GPTAdmin Node did not become healthy at $HubUrl/healthz." }
        $deadline = [DateTime]::UtcNow.AddSeconds(30)
        while (-not @(Get-OwnedProcess -Name $runtimeName -Path $CurrentExe).Count -and [DateTime]::UtcNow -lt $deadline) {
            Start-Sleep -Milliseconds 250
        }
        Start-Sleep -Seconds 3
        if (@(Get-OwnedProcess -Name $runtimeName -Path $CurrentExe).Count -lt 1) {
            throw "$runtimeName did not remain running after install. Check $LogDir\$runtimeBase.task.log"
        }
    }
} catch {
    if ($legacyLaunchers.Count) {
        if ($UserMode) { Remove-Item -LiteralPath $StartupScript -Force -ErrorAction SilentlyContinue }
        else { Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue }
        Stop-OwnedRuntime -Name $runtimeName -Path $CurrentExe
        foreach ($old in $legacyLaunchers) {
            if ($old.User) { Start-Process -FilePath $old.Batch -WindowStyle Hidden }
            else { Start-ScheduledTask -TaskName $old.Name }
        }
    }
    throw
}
foreach ($old in $legacyLaunchers) {
    if ($old.User) { Remove-Item -LiteralPath $old.Path -Force }
    else { Unregister-ScheduledTask -TaskName $old.Name -Confirm:$false }
}
Write-Host "Installed GPT Admin role $Role"
Write-Host "TaskName: $TaskName"
Write-Host "InstallDir: $InstallDir"
Write-Host "Exe: $CurrentExe"
Write-Host "HubUrl: $HubUrl"
if ($LocalHub) {
    Write-Host "AdminUrl: $HubUrl/admin"
    Write-Host "CredentialsFile: $CredentialsFile"
    Write-Host "MCP clients: use the Connect page at $HubUrl/connect"
} else {
    Write-Host "NextStep: the Hub lists this machine as pending server 'shell:$ShellmcpName'; ask the Hub admin to approve it."
}
