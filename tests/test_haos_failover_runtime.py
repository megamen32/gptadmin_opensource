from pathlib import Path

from scripts.gptadmin_failover_watchdog import build_frpc_config, reclaim_secret


ROOT = Path(__file__).resolve().parents[1]
ADDON = ROOT / "deploy/homeassistant/gptadmin_hub_standby"


def test_haos_addon_packages_and_starts_the_physical_failover_path() -> None:
    """The HAOS standby must own proxy, watchdog, and FRP takeover runtime."""

    dockerfile = (ADDON / "Dockerfile").read_text(encoding="utf-8")
    run_script = (ADDON / "run.sh").read_text(encoding="utf-8")

    assert "COPY frpc /usr/local/bin/frpc" in dockerfile
    assert "COPY gptadmin_failover_watchdog.py" in dockerfile
    assert "gptadmin_failover_proxy.py" in dockerfile
    assert "gptadmin_failover_runtime.py" in dockerfile
    assert "gptadmin_hub &" in run_script
    assert "gptadmin_failover_runtime.py" in run_script


def test_haos_runtime_promotes_proxy_port_and_runs_without_systemd() -> None:
    """The add-on runtime must use the configured proxy port without systemd."""

    runtime = (ADDON / "gptadmin_failover_runtime.py").read_text(encoding="utf-8")

    assert "--hub-service" in runtime
    assert '"none"' in runtime
    assert "--frpc-service" in runtime
    assert "--listen" in runtime
    assert "9101" in runtime
    assert "gptadmin_failover_watchdog.py" in runtime
    assert "gptadmin_failover_proxy.py" in runtime


def test_frpc_client_config_does_not_merge_multiple_servers_into_one_toml() -> None:
    """Each FRP server needs its own v0.64 client config/process."""

    bundle = {
        "tunnel": {
            "frp": {
                "token": "test-token",
                "subdomain": "fallback",
                "domain": "example.test",
                "endpoints": ["primary:7000", "backup:27000"],
            }
        }
    }

    config = build_frpc_config(bundle, 9101, "shell:haos")

    assert config.count("serverAddr =") == 1
    assert config.count("[auth]") == 1
    assert config.count("[[proxies]]") == 1


def test_reclaim_uses_existing_runtime_bridge_key_when_state_is_secret_safe(monkeypatch) -> None:
    """The sanitized state bundle must not disable signed reclaim verification."""

    monkeypatch.setenv("MCP_BRIDGE_KEY", "existing-bridge-key")

    assert reclaim_secret({}) == "existing-bridge-key"


def test_runtime_does_not_capture_watchdog_pipes_while_frpc_is_detached() -> None:
    """Detached FRP children must not block the next watchdog tick on EOF."""

    runtime = (ADDON / "gptadmin_failover_runtime.py").read_text(encoding="utf-8")

    assert "capture_output=True" not in runtime


def test_reclaim_clears_promotion_cooldown_for_the_next_outage() -> None:
    """A later primary outage must be eligible immediately after reclaim."""

    watchdog = (ROOT / "scripts/gptadmin_failover_watchdog.py").read_text(encoding="utf-8")

    assert 'runtime["last_promotion_at"] = 0' in watchdog
