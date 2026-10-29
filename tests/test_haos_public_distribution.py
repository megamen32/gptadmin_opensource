from __future__ import annotations

import json
import re
import subprocess
from pathlib import Path
import importlib

import pytest

ROOT = Path(__file__).resolve().parents[1]
DEPLOY_TARGET = "home" + "assistant"
if not (ROOT / "deploy" / DEPLOY_TARGET).is_dir():
    DEPLOY_TARGET = "server01"
failover_config = importlib.import_module(f"deploy.{DEPLOY_TARGET}.gptadmin_hub_standby.gptadmin_failover_config")
build_failover_config = failover_config.build_failover_config
build_failover_state = failover_config.build_failover_state
ensure_internal_secrets = failover_config.ensure_internal_secrets


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
    source = ROOT / "deploy" / DEPLOY_TARGET / "gptadmin_hub_standby"
    assert not (source / "config.yaml").exists()
    assert (source / "config.yaml.template").exists()
    assert "gptadmin_failover_config.py" in (source / "Dockerfile").read_text()
    assert "gptadmin_failover_config.py" in (source / "run.sh").read_text()


def test_existing_client_bearers_survive_internal_secret_initialization(tmp_path: Path) -> None:
    path = tmp_path / "internal-secrets.json"
    path.write_text(json.dumps({"codex_mcp_bearer": "provisioned-test-bearer"}))
    assert ensure_internal_secrets(path)["codex_mcp_bearer"] == "provisioned-test-bearer"
    assert json.loads(path.read_text())["codex_mcp_bearer"] == "provisioned-test-bearer"


def test_haos_member_mode_is_a_single_executor_node_with_legacy_rollout_default() -> None:
    source = ROOT / "deploy" / DEPLOY_TARGET / "gptadmin_hub_standby"
    config = (source / "public_config.yaml").read_text(encoding="utf-8")
    run_script = (source / "run.sh").read_text(encoding="utf-8")
    dockerfile = (source / "Dockerfile.public").read_text(encoding="utf-8")
    workflow = (source / "public_publish_image_workflow.yml").read_text(encoding="utf-8")

    version = re.search(r'^version: "([0-9]+\.[0-9]+\.[0-9]+)"$', config, re.MULTILINE)
    assert version is not None
    changelog = (source / "public_CHANGELOG.md").read_text(encoding="utf-8")
    assert f"## {version.group(1)}" in changelog
    assert 'node_mode: "legacy"' in config
    assert 'node_mode: str' in config
    assert 'node_mode: "member"' not in config
    assert 'export GPTADMIN_AUTH_MODE=reader' in run_script
    assert 'export GPTADMIN_NODE_IDENTITY=/data/config/membership/node-identity.json' in run_script
    assert "jqr '.failover.primary_public_url'" in run_script
    assert '-z "$GPTADMIN_NODE_SEED"' in run_script
    assert 'export SHELL_IDENTITY_DIR=/data/identity' in run_script
    assert 'export SHELL_SPOOL_DIR=/data/spool' in run_script
    assert 'export SHELL_OUTBOX_DIR=/data/outbox' in run_script
    assert 'NODE_BINARY=/usr/local/bin/gptadmin-node' in run_script
    assert 'gptadmin-node' in dockerfile
    for package in ('docker-cli', 'iproute2', 'iputils', 'bind-tools', 'coreutils', 'procps', 'sudo'):
        assert package in dockerfile
    assert './cmd/gptadmin-node' in workflow
    for capability in ('homeassistant_api: true', 'hassio_api: true', 'docker_api: true', 'full_access: true'):
        assert capability in config
    assert 'server01_api:' not in config
    for path in ('- config:rw', '- ssl:rw', '- addons:rw', '- share:rw', '- backup:rw', '- media:rw'):
        assert path in config


def test_standby_template_preserves_existing_client_bearer_inputs() -> None:
    source = ROOT / "deploy" / DEPLOY_TARGET / "gptadmin_hub_standby"
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
    assert (output / ".github/workflows/publish-haos-image.yml").is_file()
    image_workflow = (output / ".github/workflows/publish-haos-image.yml").read_text(encoding="utf-8")
    assert "packages: write" in image_workflow
    assert "github.token" in image_workflow
    assert image_workflow.index("docker/login-action") < image_workflow.index("Reject an existing immutable app image version")
    assert "SOURCE.json" in image_workflow
    assert "secrets." not in image_workflow
    assert "fetch --depth=1 origin \"$SOURCE_REF\"" in image_workflow
    assert "public_standby_index.html" in image_workflow
    assert "refusing to overwrite" in image_workflow
    assert not list(output.rglob("failover_state.json"))
    assert not list(output.rglob("frpc"))
    source = json.loads((output / "SOURCE.json").read_text(encoding="utf-8"))
    public_config = (root / "deploy/server01/gptadmin_hub_standby/public_config.yaml").read_text(encoding="utf-8")
    version = re.search(r'^version:\s*["\']([^"\']+)["\']\s*$', public_config, re.MULTILINE)
    assert version is not None
    assert source["app_version"] == version.group(1)
    image = re.search(r'^image:\s*(\S+)\s*$', public_config, re.MULTILINE)
    assert image is not None
    assert source["image"] == image.group(1)
    public_text = "\n".join(path.read_text(encoding="utf-8") for path in output.rglob("*") if path.is_file())
    assert "192.168.2." not in public_text
    assert "/etc/gptadmin" not in public_text
    assert "__CTL_TOKEN__" not in public_text
