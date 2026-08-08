from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from deploy.homeassistant.gptadmin_hub_standby.gptadmin_failover_config import (
    build_failover_config,
    build_failover_state,
    ensure_internal_secrets,
)


def sample_options() -> dict[str, object]:
    return {
        "failover_frp_token": "test-only-token",
        "failover": {
            "primary_health_url": "https://hub.example.invalid/healthz",
            "primary_public_url": "https://hub.example.invalid",
            "node_id": "shell:haos",
            "rank": 1,
            "fail_count_base": 3,
            "local_hub_port": 9001,
            "endpoints": ["edge-a.example.invalid:7000", "edge-b.example.invalid:7000"],
            "subdomain": "gptadmin",
            "domain": "example.invalid",
        },
    }


def test_build_failover_config_contains_only_routing_metadata() -> None:
    config = build_failover_config(sample_options())

    assert config["primary_health_url"] == "https://hub.example.invalid/healthz"
    assert config["primary_public_url"] == "https://hub.example.invalid"
    assert config["nodes"] == [
        {
            "server_id": "shell:haos",
            "rank": 1,
            "enabled": True,
            "hub_url": "http://127.0.0.1:9001",
            "local_hub_port": 9001,
        }
    ]
    assert "token" not in str(config).lower()


def test_build_failover_state_excludes_frp_and_reclaim_secrets() -> None:
    state = build_failover_state(sample_options())
    frp = state["tunnel"]["frp"]

    assert frp["endpoints"] == ["edge-a.example.invalid:7000", "edge-b.example.invalid:7000"]
    assert frp["subdomain"] == "gptadmin"
    assert frp["domain"] == "example.invalid"
    assert "token" not in str(state).lower()
    assert "secrets" not in state


def test_missing_public_failover_fields_fail_explicitly() -> None:
    options = sample_options()
    options["failover"] = {"node_id": "shell:haos"}

    with pytest.raises(ValueError, match="primary_health_url"):
        build_failover_config(options)


def test_internal_credentials_are_generated_and_persisted(tmp_path: Path) -> None:
    path = tmp_path / "internal-secrets.json"

    first = ensure_internal_secrets(path)
    second = ensure_internal_secrets(path)

    assert set(first) == {
        "ctl_token",
        "mcp_relay_agent_token",
        "shellmcp_token",
        "oauth_client_secret",
        "mcp_bridge_key",
    }
    assert first == second
    assert path.stat().st_mode & 0o777 == 0o600


def test_public_app_source_is_not_the_instance_template() -> None:
    source = Path(__file__).parents[1] / "deploy/homeassistant/gptadmin_hub_standby"
    assert not (source / "config.yaml").exists()
    assert (source / "config.yaml.template").exists()
    assert "gptadmin_failover_config.py" in (source / "Dockerfile").read_text()
    assert "gptadmin_failover_config.py" in (source / "run.sh").read_text()


def test_standby_template_preserves_existing_client_bearer_inputs() -> None:
    source = Path(__file__).parents[1] / "deploy/homeassistant/gptadmin_hub_standby"
    template = (source / "config.yaml.template").read_text(encoding="utf-8")
    run_script = (source / "run.sh").read_text(encoding="utf-8")

    for client in ("CODEX", "CLAUDE", "CUSTOM", "OPENCODE", "HERMES", "OPENCLAW", "VSCODE", "ZED", "AVAILABILITY_MONITOR"):
        assert f"__GPTADMIN_{client}_MCP_BEARER__" in template
        assert f"GPTADMIN_{client}_MCP_BEARER" in run_script


def test_exporter_creates_only_the_public_allowlist(tmp_path: Path) -> None:
    root = Path(__file__).parents[1]
    output = tmp_path / "gptadmin-haos-addons"
    subprocess.run(
        [
            str(root / "scripts/export_haos_app_repository.sh"),
            "--output",
            str(output),
            "--source-ref",
            "test-source-ref",
        ],
        cwd=root,
        check=True,
    )

    assert (output / "repository.yaml").is_file()
    assert (output / "gptadmin_hub_standby/config.yaml").is_file()
    assert (output / "gptadmin_hub_standby/Dockerfile").is_file()
    assert not list(output.rglob("failover_state.json"))
    assert not list(output.rglob("frpc"))
    public_text = "\n".join(path.read_text(encoding="utf-8") for path in output.rglob("*") if path.is_file())
    assert "192.168.2." not in public_text
    assert "/etc/gptadmin" not in public_text
    assert "__CTL_TOKEN__" not in public_text
