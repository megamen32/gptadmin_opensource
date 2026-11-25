from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_windows_installer_never_echoes_shell_token() -> None:
    """Normal Windows completion output must not expose an agent credential."""

    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert 'Write-Host "Token:' not in source
    assert 'Write-Host "SHELLMCP_TOKEN:' not in source


def test_public_install_and_adapter_docs_use_oauth_product_language() -> None:
    """Public onboarding must direct users to the Hub connection flow."""

    install_docs = (ROOT / "docs" / "INSTALL_PATHS.md").read_text(encoding="utf-8")
    adapter_docs = (ROOT / "docs" / "ADAPTERS.md").read_text(encoding="utf-8")
    combined = install_docs + "\n" + adapter_docs
    assert "CTL_TOKEN" not in combined
    assert "SHELLMCP_TOKEN" not in combined
    assert "/connect" in combined or "OAuth" in combined


def test_windows_user_install_is_task_scheduler_independent() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "if ($UserMode) {\n        Install-UserStartup -Batch $Batch -Startup $Startup\n        return 'startup'" in source
    assert "Per-user startup is deliberately Task-Scheduler-free" in source
    assert "if ($UserMode) {\n        # User installs must not depend on Task Scheduler access" in source


def test_windows_installer_accepts_exact_local_candidate_artifact() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "Test-Path -LiteralPath $PackageUrl" in source
    assert "Copy-Item -LiteralPath $PackageUrl -Destination $archive -Force" in source


def test_windows_installer_bootstraps_a_local_hub_by_default() -> None:
    """A bare Windows install is a self-contained local Hub + agent."""

    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "function New-RandomToken" in source
    assert "function Test-LoopbackUrl" in source
    assert "$LocalHub = (-not $HubUrl) -or (Test-LoopbackUrl $HubUrl)" in source
    assert "if (-not $ShellmcpToken) { $ShellmcpToken = New-RandomToken }" in source
    assert "$HubExe = Join-Path $BinDir 'gptadmin-hub.exe'" in source
    assert 'gptadmin-hub.exe not found in package' in source
    assert '"CTL_TOKEN=$CtlToken"' in source
    assert '"ADMIN_PASSWORD=$AdminPassword"' in source
    assert '"OAUTH_CLIENT_SECRET=$OAuthClientSecret"' in source
    assert '"GPTADMIN_CONFIG_DIR=$ConfigDir"' in source
    assert '"GPTADMIN_PUBLIC_DIR=$PublicDir"' in source
    assert '"PUBLIC_ORIGIN=$HubUrl"' in source
    assert '"GPTADMIN_STANDALONE=1"' in source
    # The admin UI payloads ride in the same archive and land in PublicDir.
    assert "@('admin', 'openapi.yaml', 'custom-gpt-instructions.md')" in source
    # The freshly installed Hub must answer /healthz before install succeeds.
    assert "did not become healthy at $HubUrl/healthz" in source
    # The person signs in with the locally generated AdminPassword; the
    # local agent is trusted automatically (GPTADMIN_STANDALONE).
    assert "AdminUrl: $HubUrl/admin" in source
    assert "AdminPassword: $AdminPassword" in source
    assert "CredentialsFile: $CredentialsFile" in source
    assert "trusted automatically" in source


def test_windows_installer_remote_agent_self_enrolls() -> None:
    """An explicit remote Hub keeps the no-secret agent enrollment path."""

    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "unregistered random credential" not in source
    assert "NextStep: the Hub lists this machine as pending server 'shell:$ShellmcpName'" in source


def test_windows_installer_rejects_multiline_or_html_credentials() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "must be one non-whitespace line" in source
    assert "$ShellmcpToken.Length -lt 16" in source
    assert "$ShellmcpToken -match '\\s|[<>]'" in source


def test_windows_installer_requires_real_process_after_start() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    check = "ShellMCP did not remain running after install"
    assert check in source
    assert source.index("Start-Sleep -Seconds 3") < source.index(check)
    assert source.index(check) < source.index("Installed GPT Admin")


def test_windows_installer_isolates_agent_identity_and_name_per_install() -> None:
    """A stale machine-wide identity must not replace a new install's name."""

    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "$IdentityDir = Join-Path $InstallDir 'identity'" in source
    assert "SHELLMCP_IDENTITY_DIR=$IdentityDir" in source
    assert "SHELL_IDENTITY_DIR=$IdentityDir" in source
    assert "SHELLMCP_NAME=$ShellmcpName" in source
    assert "SHELL_NAME=$ShellmcpName" in source


def test_public_install_win_matches_deploy() -> None:
    """The public mirror must serve exactly the reviewed deploy copy."""

    deploy = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    public = (ROOT / "public" / "install_win.ps1").read_text(encoding="utf-8")
    assert deploy == public


def test_windows_zip_carries_admin_payload() -> None:
    """The package build must ship the admin UI with the Windows binaries."""

    source = (ROOT / "tools" / "build.sh").read_text(encoding="utf-8")
    assert "public/admin" in source
    assert "custom-gpt-instructions.md" in source
    assert "openapi.yaml" in source


def test_windows_installer_scopes_pending_results_per_install() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "$SpoolDir = Join-Path $InstallDir 'spool'" in source
    assert "SHELL_SPOOL_DIR=$SpoolDir" in source
    assert "SHELLMCP_SPOOL_DIR=$SpoolDir" in source
    assert "SHELL_OUTBOX_DIR=$OutboxDir" in source
    assert "SHELLMCP_OUTBOX_DIR=$OutboxDir" in source


def test_windows_isolated_install_rejects_collisions_before_download() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "[switch]$Isolated" in source
    assert "function Assert-IsolatedTarget" in source
    assert source.index("Assert-IsolatedTarget\nDownload-And-InstallArtifact") > source.index("function Assert-IsolatedTarget")
    assert "Isolated install refuses an existing bin directory" in source
    assert "Isolated install refuses an existing scheduled task" in source


def test_windows_isolated_install_overrides_inherited_aliases_and_update_policy() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    for key in ("SHELL_TOKEN", "SHELL_PORT", "SHELL_HOST", "SHELL_URL", "QUEUE_URL", "SHELL_MODE", "SHELLMCP_MODE", "SHELLMCP_MCP_CONFIG", "SHELLMCP_LEGACY_MANIFEST_UPDATE"):
        assert '"' + key + '=' in source
    assert "[ValidateRange(0, 2147483647)]" in source
    assert "[int]$LatestBuild = 0" in source
    assert "[switch]$DisableSelfRepair" in source
    assert "SHELLMCP_LATEST_BUILD=" in source
    assert "SHELLMCP_SELF_REPAIR_DISABLE=" in source
    assert "Set-Content -LiteralPath $McpConfigFile -Value '[]'" in source

def test_windows_isolated_install_requires_package_and_defaults_repair_off() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "'ShellmcpName', 'PackageUrl')" in source
    assert "[string]::IsNullOrWhiteSpace($PackageUrl)" in source
    assert "if (-not $InstallerArguments.ContainsKey('DisableSelfRepair')) { $DisableSelfRepair = $true }" in source
    assert "build 1156 or newer" in source
