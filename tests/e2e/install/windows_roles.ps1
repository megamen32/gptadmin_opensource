#Requires -Version 5.1
param([string]$PackageZip, [string]$ClientZip)
$ErrorActionPreference = 'Stop'
function Read-Settings([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        $parts = $line -split '=', 2
        if ($parts.Length -eq 2) { $values[$parts[0]] = $parts[1] }
    }
    return $values
}
function Invoke-CanaryHttp([string]$Uri, [hashtable]$Headers, [string]$Body = '') {
    $request = [Net.HttpWebRequest]::Create($Uri)
    $request.Proxy = $null
    $request.AllowAutoRedirect = $false
    $request.Timeout = 30000
    foreach ($key in $Headers.Keys) { $request.Headers[$key] = $Headers[$key] }
    if ($Body) {
        $request.Method = 'POST'
        $request.ContentType = 'application/json'
        $bytes = [Text.Encoding]::UTF8.GetBytes($Body)
        $request.ContentLength = $bytes.Length
        $stream = $request.GetRequestStream()
        try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
    }
    try { $response = $request.GetResponse() }
    catch [Net.WebException] {
        $response = $_.Exception.Response
        if (-not $response) { throw }
    }
    try {
        $reader = [IO.StreamReader]::new($response.GetResponseStream())
        try { return [pscustomobject]@{ StatusCode = [int]$response.StatusCode; Content = $reader.ReadToEnd() } }
        finally { $reader.Dispose() }
    } finally { $response.Dispose() }
}
function Wait-Server([string]$Name, [string]$Status, [hashtable]$Headers) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $inventory = Invoke-CanaryHttp "$url/servers" $Headers
        if ($inventory.StatusCode -eq 200) {
            $entries = ($inventory.Content | ConvertFrom-Json).servers
            if (@($entries | Where-Object { $_.name -eq $Name -and $_.status -eq $Status }).Count) { return }
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Windows server $Name did not reach status $Status."
}
$root = Join-Path ([IO.Path]::GetTempPath()) ("gptadmin-windows-role-" + [Guid]::NewGuid().ToString('N'))
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$installer = Join-Path $repo 'deploy/install_win.ps1'
$node = Join-Path $root 'node root'
$leaf = Join-Path $root 'executor'
$oldAppData = $env:APPDATA
$env:APPDATA = Join-Path $root 'appdata'
New-Item -ItemType Directory -Force $root | Out-Null
function Get-TestPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return $listener.LocalEndpoint.Port } finally { $listener.Stop() }
}
$port = Get-TestPort
$url = "http://127.0.0.1:$port"
$name = 'win-role-canary'
try {
    if (-not $PackageZip) {
        $PackageZip = Join-Path $root 'real-windows-binaries.zip'
        $bin = Join-Path $repo 'build/windows'
        Compress-Archive -Path "$bin/gptadmin-node.exe","$bin/shellmcp.exe" -DestinationPath $PackageZip
    }
    if (-not $ClientZip) {
        $ClientZip = Join-Path $root 'shellmcp-only.zip'
        $bin = Join-Path $repo 'build/windows/shellmcp.exe'
        Compress-Archive -Path $bin -DestinationPath $ClientZip
    }
    & $installer -User -InstallDir $node -HubPort $port -ShellmcpPort (Get-TestPort) -ShellmcpBind 127.0.0.1 -ShellmcpName $name -TaskName 'gptadmin-ci-node' -PackageUrl $PackageZip
    if (-not (Test-Path "$node/bin/gptadmin-node.exe") -or (Test-Path "$node/bin/shellmcp.exe") -or (Test-Path "$node/bin/gptadmin-hub.exe")) {
        throw 'Full installation did not select only the unified binary.'
    }
    $cfg = Read-Settings "$node/node.env"
    $headers = @{Authorization = "Bearer $($cfg.CTL_TOKEN)"}
    Wait-Server $name 'online' $headers
    $body = @{target="shell:$name"; cmd='echo windows-unified-ok > receipt.txt'; cwd=$node; timeout=10} | ConvertTo-Json
    $response = Invoke-CanaryHttp "$url/mcp-relay/shell_exec" $headers $body
    if ($response.StatusCode -eq 428) {
        $challenge = $response.Content | ConvertFrom-Json
        if ($challenge.executed -ne $false -or -not $challenge.context) { throw 'Invalid pre-execution instruction challenge.' }
        $headers['X-GPTAdmin-Context'] = $challenge.context
        $response = Invoke-CanaryHttp "$url/mcp-relay/shell_exec" $headers $body
    }
    if ($response.StatusCode -ne 200 -or -not (Test-Path "$node/receipt.txt") -or
        (Get-Content -LiteralPath "$node/receipt.txt" -Raw).Trim() -ne 'windows-unified-ok') {
        throw "Unified Windows Node failed its local command. HTTP $($response.StatusCode). $($response.Content)"
    }
    & $installer -User -Role executor -HubUrl $url -InstallDir $leaf -ShellmcpPort (Get-TestPort) -ShellmcpBind 127.0.0.1 -ShellmcpName 'win-role-leaf' -TaskName 'gptadmin-ci-leaf' -PackageUrl $ClientZip -DisableSelfRepair
    if (-not (Test-Path "$leaf/bin/shellmcp.exe") -or (Test-Path "$leaf/node.env") -or (Test-Path "$leaf/bin/gptadmin-node.exe") -or (Test-Path "$leaf/ADMIN-CREDENTIALS.txt")) {
        throw 'Executor installation contains a control-plane runtime or credentials.'
    }
    $leafConfig = Read-Settings "$leaf/shellmcp.env"
    $originalToken = $leafConfig.SHELLMCP_TOKEN
    if (-not $originalToken -or $leafConfig.ADMIN_PASSWORD -or $leafConfig.CTL_TOKEN) { throw 'Unexpected executor credentials.' }
    Wait-Server 'win-role-leaf' 'awaiting_approval' $headers
    $denied = Invoke-CanaryHttp "$url/admin/api/instruction-sets" @{Authorization="Bearer $originalToken"}
    if ($denied.StatusCode -notin @(401, 403)) { throw "Executor admission to admin API produced HTTP $($denied.StatusCode)." }
    & $installer -User -InstallDir $leaf -PackageUrl $ClientZip -DisableSelfRepair
    $updated = Read-Settings "$leaf/shellmcp.env"
    if ($updated.SHELLMCP_TOKEN -ne $originalToken -or $updated.HUB_URL -ne $url -or (Test-Path "$leaf/node.env")) { throw 'Executor update changed identity, manager or role.' }
    Write-Host 'Windows Node command completed, executor enrollment pending, admin access denied, executor identity survived update.'
} catch {
    foreach ($logPath in @("$leaf/logs/shellmcp.task.log", "$node/logs/node.task.log")) {
        if (Test-Path $logPath) {
            Get-Content -LiteralPath $logPath -Tail 12 | Where-Object { $_ -match '(?i)(fatal|listen tcp|address already in use)' } |
                ForEach-Object { Write-Warning ($_ -replace '(?i)(token|secret|password)=[^ ]+', '$1=<redacted>') }
        }
    }
    throw
} finally {
    if (Test-Path "$leaf/shellmcp.env") { & $installer -User -Uninstall -InstallDir $leaf -TaskName 'gptadmin-ci-leaf' }
    if (Test-Path "$node/node.env") { & $installer -User -Uninstall -InstallDir $node -TaskName 'gptadmin-ci-node' }
    $env:APPDATA = $oldAppData
    Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction Continue
}
