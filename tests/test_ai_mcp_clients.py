"""Regression coverage for zero-config local MCP client registration."""

from __future__ import annotations

import base64
import json
from types import SimpleNamespace

import pytest

import cli


def _client_env() -> dict[str, str]:
    """Return the minimal public Hub environment required by MCP clients."""
    return {
        "HUB_URL": "http://127.0.0.1:9001",
        "HUB_PUBLIC_URL": "https://hub.example.test",
        "OAUTH_CLIENT_SECRET": "test-signing-secret",
        "ADMIN_PASSWORD": "test-admin-password",
    }


def test_mcp_clients_use_the_canonical_public_hub_url() -> None:
    """Desktop clients must not receive the Hub's loopback-only service URL."""
    assert cli._mcp_client_url(_client_env()) == "https://hub.example.test/mcp"


def test_readonly_cli_token_has_inspection_scope_without_exec() -> None:
    """The CLI fallback can issue a token that cannot request command execution."""
    token = cli.make_mcp_bearer_token(_client_env(), "chatgpt", access_mode="readonly")
    payload_segment = token.split(".")[1]
    payload = json.loads(base64.urlsafe_b64decode(payload_segment + "=" * (-len(payload_segment) % 4)))

    assert payload["access_mode"] == "readonly"
    assert payload["scope"] == "gptadmin.read gptadmin.inspect"
    assert "gptadmin.exec" not in payload["scope"]


def test_configure_all_supported_clients_registers_vscode(monkeypatch: pytest.MonkeyPatch) -> None:
    """One registration pass configures every supported local MCP client."""
    registered: dict[str, tuple[str, str]] = {}

    monkeypatch.setattr(cli, "env_remove_keys", lambda _keys: None)
    monkeypatch.setattr(cli, "env_set_many", lambda _values: None)
    monkeypatch.setattr(cli, "_set_process_env_for_gui_clients", lambda _tokens: None)
    monkeypatch.setattr(cli, "_configure_claude_code_mcp", lambda url, token: registered.setdefault("claude-code", (url, token)) and "ok")
    monkeypatch.setattr(cli, "_configure_codex_mcp", lambda url, token: registered.setdefault("codex", (url, token)) and "ok")
    monkeypatch.setattr(cli, "_configure_opencode_mcp", lambda url, token: registered.setdefault("opencode", (url, token)) and "ok")
    monkeypatch.setattr(cli, "_configure_vscode_mcp", lambda url, token: registered.setdefault("vscode", (url, token)) and "ok")

    results = cli.configure_ai_mcp_clients(_client_env(), print_custom=False)

    assert set(registered) == {"claude-code", "codex", "opencode", "vscode"}
    assert {url for url, _token in registered.values()} == {"https://hub.example.test/mcp"}
    assert results["_url"] == "https://hub.example.test/mcp"
    assert "_custom_token" not in results


def test_vscode_registration_uses_its_global_mcp_cli_contract(monkeypatch: pytest.MonkeyPatch) -> None:
    """VS Code must receive a named remote HTTP server in its user profile."""
    calls: list[list[str]] = []
    monkeypatch.setattr(cli.shutil, "which", lambda name: "/usr/bin/code" if name == "code" else None)
    monkeypatch.setattr(
        cli,
        "_run_quiet",
        lambda command, env=None: calls.append(command) or SimpleNamespace(returncode=0, stdout="", stderr=""),
    )

    assert cli._configure_vscode_mcp("https://hub.example.test/mcp", "hidden-token") == "ok"
    assert calls[0][:2] == ["code", "--add-mcp"]
    payload = json.loads(calls[0][2])
    assert payload == {
        "name": "gptadmin",
        "type": "http",
        "url": "https://hub.example.test/mcp",
        "headers": {"Authorization": "Bearer hidden-token"},
    }


def test_auto_registration_never_prints_a_bearer_token(monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]) -> None:
    """Automatic setup reports client status without leaking a reusable secret."""
    monkeypatch.setattr(
        cli,
        "configure_ai_mcp_clients",
        lambda _env, **_kwargs: {"codex": "ok", "_url": "https://hub.example.test/mcp"},
    )

    cli.auto_configure_ai_mcp_clients(_client_env(), install_hub=True)

    output = capsys.readouterr().out
    assert "codex=ok" in output
    assert "Authorization" not in output
    assert "Bearer" not in output
