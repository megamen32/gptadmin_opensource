"""Tests for the curated MCP capability catalog contract."""

from __future__ import annotations

import argparse
import json

import pytest

import cli


def test_mcp_catalog_is_attributed_and_machine_readable(monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]) -> None:
    """Catalog output must expose provenance and risk before activation."""

    cli.cmd_mcp_catalog(argparse.Namespace(json=True))
    payload = json.loads(capsys.readouterr().out)
    assert payload["catalog_version"]
    assert payload["source"]
    assert payload["definitions"]
    assert payload["signature"]["algorithm"] == "Ed25519"
    assert payload["signature"]["verified"] is True
    assert payload["catalog_digest_sha256"]
    for definition in payload["definitions"]:
        assert definition["id"]
        assert definition["version"]
        assert definition["provenance"]
        assert definition["scopes"]
        assert "network_needs" in definition
        assert definition["risk_level"] in {"low", "medium", "high"}
        assert definition["maintenance_owner"]


def test_catalog_signature_rejects_tampered_definition() -> None:
    """The bundled activation boundary must fail closed if catalog metadata changes."""

    payload = cli._mcp_catalog_payload()
    tampered = json.loads(json.dumps(payload))
    tampered["definitions"][0]["risk_level"] = "high"
    with pytest.raises(ValueError, match="catalog signature"):
        cli._verify_mcp_catalog_payload(tampered)


def test_curated_mcp_install_requires_explicit_capability_acceptance(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """A catalog-bound install must not activate without explicit acceptance."""

    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", tmp_path / "mcp.json")
    args = argparse.Namespace(
        name="safe-demo",
        command="npx",
        args=["-y", "example-mcp"],
        url=None,
        stdio_format=None,
        cwd=None,
        env=[],
        disabled=False,
        force=False,
        install=True,
        status=False,
        agent_id=None,
        run_as_user=None,
        hub_url=None,
        catalog_id="gptadmin-safe-demo",
        accept_capability=False,
    )
    with pytest.raises(SystemExit):
        cli.cmd_mcp_add(args)
    assert not (tmp_path / "mcp.json").exists()


def test_curated_mcp_binding_persists_provenance_before_activation(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """A reviewed catalog binding is stored with the configured MCP entry."""

    monkeypatch.setattr(cli, "need_root", lambda: None)
    config_file = tmp_path / "mcp.json"
    token_file = tmp_path / "mcp-relay.token"
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", config_file)
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", tmp_path / "mcp-agents.d")
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "test-managed-token"})
    args = argparse.Namespace(
        name="safe-demo",
        command="npx",
        args=["-y", "example-mcp"],
        url=None,
        stdio_format=None,
        cwd=None,
        env=[],
        disabled=False,
        force=False,
        install=False,
        status=False,
        agent_id=None,
        run_as_user=None,
        hub_url=None,
        catalog_id="gptadmin-safe-demo",
        accept_capability=False,
    )
    cli.cmd_mcp_add(args)
    saved = cli._json_read(config_file, {})["mcpServers"]["safe-demo"]
    assert saved["catalog_id"] == "gptadmin-safe-demo"
    assert saved["catalog_version"] == "1.0.0"
    assert saved["catalog_provenance"] == "bundled:gptadmin-hub"
