from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_windows_installer_never_echoes_shell_token() -> None:
    """Normal Windows completion output must not expose a bearer credential."""

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
    assert "if ($UserMode) {\n        Install-UserStartup\n        return 'startup'" in source
    assert "Per-user startup is deliberately Task-Scheduler-free" in source
    assert "if ($UserMode) {\n        # User installs must not depend on Task Scheduler access" in source


def test_windows_installer_accepts_exact_local_candidate_artifact() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "Test-Path -LiteralPath $PackageUrl" in source
    assert "Copy-Item -LiteralPath $PackageUrl -Destination $archive -Force" in source


def test_windows_installer_fails_closed_without_hub_agent_credential() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "unregistered random credential" in source
    assert "if (-not $ShellmcpToken) { $ShellmcpToken =" not in source


def test_windows_installer_rejects_multiline_or_html_credentials() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    assert "must be one non-whitespace line" in source
    assert "$ShellmcpToken.Length -lt 16" in source
    assert "$ShellmcpToken -match '\\s|[<>]'" in source


def test_windows_installer_requires_real_process_after_start() -> None:
    source = (ROOT / "deploy" / "install_win.ps1").read_text(encoding="utf-8")
    check = "ShellMCP did not remain running after install"
    assert check in source
    assert source.index("Start-Sleep -Seconds 3") < source.index(check) < source.index('Write-Host "Installed GPT Admin shellmcp"')
