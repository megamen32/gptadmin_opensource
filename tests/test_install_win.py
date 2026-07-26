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
