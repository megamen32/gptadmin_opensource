from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "deploy" / "windows-shellmcp-autostart-reconcile.ps1"


def source() -> str:
    return SCRIPT.read_text(encoding="utf-8")


def test_windows_reconciler_uses_one_system_at_startup_owner() -> None:
    script = source()

    assert "gptadmin-shellmcp" in script
    assert "New-ScheduledTaskTrigger -AtStartup" in script
    assert "New-ScheduledTaskPrincipal -UserId 'SYSTEM'" in script
    assert "Register-ScheduledTask" in script
    assert "SHELLMCP_IDENTITY_DIR" in script
    assert "SHELLMCP_NAME=BeyondInfinity" in script


def test_windows_reconciler_exports_and_disables_legacy_without_deleting() -> None:
    script = source()

    assert "gptadmin-rootd" in script
    assert "GPTAdminRootd" in script
    assert "Export-ScheduledTask" in script
    assert "Disable-ScheduledTask" in script
    assert "Unregister-ScheduledTask -TaskName $name" not in script
    assert "GPTAdmin MCP BeyondInfinity-windows-mcp" in script
    assert "unrelated_task_untouched" in script


def test_windows_reconciler_has_secret_safe_receipts_and_rollback() -> None:
    script = source()

    assert "RollbackReceipt" in script
    assert "rollback" in script.lower()
    assert "Get-FileHash" in script
    assert "ConvertTo-Json" in script
    assert "Token:" not in script
    assert "ShellmcpToken" not in script
    assert "ROOTD_TOKEN" in script
    assert "REDACTED" in script


def test_windows_reconciler_accepts_explicit_current_hub_url() -> None:
    script = source()

    assert "[string]$HubUrl" in script
    assert "$effectiveHubUrl" in script
    assert "HUB_URL=$effectiveHubUrl" in script
    assert "SHELLMCP_UPDATE_MANIFEST_URL=$effectiveHubUrl/artifacts/shellmcp.json" in script


def test_windows_launcher_records_secret_safe_bootstrap_failures() -> None:
    script = source()

    assert "shellmcp.bootstrap.log" in script
    assert "bootstrap_error=" in script
    assert "env_loaded" in script
    assert "catch" in script
    assert "shellmcp.stdout.log" in script
    assert "shellmcp.stderr.log" in script
    assert "Start-Process -FilePath $exe" in script
    assert "-RedirectStandardOutput $stdout" in script
    assert "-RedirectStandardError $stderr" in script
    assert "-Wait" in script


def test_windows_current_agent_credential_is_stdin_only() -> None:
    script = source()

    assert "[switch]$AgentTokenFromStdin" in script
    assert "[Console]::In.ReadLine()" in script
    assert "credential_digest_match" in script
    assert "credential_sha256" in script
    assert "credential = 'REDACTED'" in script
    assert "[string]$AgentToken" not in script
    assert "AgentTokenFile" not in script


def test_windows_rollback_restores_preexisting_canonical_task() -> None:
    script = source()

    assert "canonical_before" in script
    assert "$receipt.canonical_before.existed" in script
    assert "Register-ScheduledTask -TaskName $receipt.canonical_task -Xml" in script


def test_windows_credential_file_is_acl_restricted() -> None:
    script = source()

    assert "SetAccessRuleProtection($true, $false)" in script
    assert "S-1-5-18" in script
    assert "S-1-5-32-544" in script
    assert "Set-Acl -LiteralPath $Path" in script
    assert "Set-SecretFileAcl -Path $EnvFile" in script


def test_windows_mutations_are_guarded_by_pre_mutation_receipt_and_automatic_rollback() -> None:
    script = source()

    assert "status = 'pre_mutation'" in script
    assert "status = 'mutation_failed'" in script
    assert "Invoke-Rollback -ReceiptPath $ReceiptPath" in script
    assert "Set-PrivatePathAcl -Path $ReceiptDir" in script
    assert "Set-PrivatePathAcl -Path $BackupDir" in script
    assert "Set-PrivatePathAcl -Path $LogDir" in script


def test_windows_rollback_stops_owner_restores_files_and_handles_new_files() -> None:
    script = source()

    assert "Stop-CanonicalOwner -TaskName $receipt.canonical_task" in script
    assert "Get-ScheduledTaskInfo" in script
    assert "failed to stop canonical task before rollback" in script
    assert "$backup.existed" in script
    assert "Remove-Item -LiteralPath $backup.path" in script
    assert "FallbackLauncherPath" in script
    assert "Unregister-ScheduledTask -TaskName $receipt.canonical_task" in script
    assert "Unregister-ScheduledTask -TaskName $name" not in script


def test_windows_rollback_normalizes_legacy_v1_receipts_safely() -> None:
    script = source()

    assert "gptadmin.windows-shellmcp-autostart/v2" in script
    assert "PSObject.Properties['existed']" in script
    assert "Join-Path $receipt.install_dir 'bin\\shellmcp.exe'" in script
