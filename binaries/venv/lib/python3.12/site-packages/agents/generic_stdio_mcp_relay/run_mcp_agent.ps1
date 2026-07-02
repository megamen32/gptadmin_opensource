param(
  [Parameter(Mandatory=$true)][string]$Config
)
$ErrorActionPreference = "Continue"
$script:LogFile = $null
function Write-Log($Message) {
  $ts = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
  $line = "$ts $Message"
  Write-Output $line
  if ($script:LogFile) { Add-Content -Path $script:LogFile -Value $line -Encoding UTF8 }
}
while ($true) {
  try {
    $cfg = Get-Content -Raw -Path $Config | ConvertFrom-Json
    $logDir = "C:\ProgramData\GPTAdmin\logs"
    if ($cfg.log_dir) { $logDir = [string]$cfg.log_dir }
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null
    $safeName = ([string]$cfg.agent_id) -replace '[^A-Za-z0-9_.-]', '-'
    $script:LogFile = Join-Path $logDir "mcp-$safeName.log"
    $root = Split-Path -Parent $MyInvocation.MyCommand.Path
    $relay = Join-Path $root "generic_stdio_mcp_relay.py"
    if ($cfg.relay_path) { $relay = [string]$cfg.relay_path }
    $python = "python"
    if ($cfg.python) { $python = [string]$cfg.python }
    $args = @($relay, "--agent-config", $Config)
    if ($cfg.cwd) { Set-Location -Path ([string]$cfg.cwd) }
    if ($cfg.env) { $cfg.env.PSObject.Properties | ForEach-Object { [Environment]::SetEnvironmentVariable($_.Name, [string]$_.Value, "Process") } }
    [Environment]::SetEnvironmentVariable("PYTHONUNBUFFERED", "1", "Process")
    Write-Log "starting $python $($args -join ' ')"
    & $python @args *>> $script:LogFile
    Write-Log "relay exited code=$LASTEXITCODE; restarting in 5s"
  } catch {
    Write-Log "wrapper error: $($_.Exception.Message); restarting in 5s"
  }
  Start-Sleep -Seconds 5
}
