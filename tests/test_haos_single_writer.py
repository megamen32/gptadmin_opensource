import hashlib
import importlib.util
import json
from pathlib import Path
import pytest

ROOT = Path(__file__).resolve().parents[1]
PATH = ROOT / "deploy/server01/gptadmin_hub_standby/gptadmin_single_writer.py"
spec = importlib.util.spec_from_file_location("gptadmin_single_writer", PATH)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

def file(path, obj):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(obj))
    path.chmod(0o600)

def fixture(tmp_path):
    config = tmp_path / "config"
    identity = tmp_path / "identity.json"
    file(identity, {"server_id": "haos-executor-id"})
    file(config / "membership/membership.json", {"writer_id": "haos-executor-id", "nodes": {"haos-executor-id": {"node_id": "haos-executor-id"}}})
    file(config / "auth-continuity/state.json", {"generation": 12, "pending": False, "slot": 0, "writer_id": "old-writer", "read_only": True})
    file(config / "auth-continuity/slot-0/mcp_tokens_state.json", {"tokens": {}})
    file(config / "auth-continuity/slot-0/oauth_clients_state.json", {"clients": {}})
    db = config / "tasks_state.sqlite"
    db.write_bytes(b"SQLite format 3\x00")
    db.chmod(0o600)
    env = {"CTL_TOKEN": "ctl", "OAUTH_CLIENT_SECRET": "signer", "MCP_RELAY_AGENT_TOKEN": "relay", "ADMIN_PASSWORD": "admin"}
    hashes = {key: hashlib.sha256(value.encode()).hexdigest() for key, value in env.items()}
    file(config / "single-writer-handoff.json", {"schema": "gptadmin.single-writer-handoff/v1", "complete": True, "writer_id": "haos-executor-id", "source_writer_id": "old-writer", "source_writer_fenced": True, "domains": sorted(module.DOMAINS), "credential_sha256": hashes})
    return config, identity, env

def test_valid_prepared_writer_and_restart(tmp_path):
    config, identity, env = fixture(tmp_path)
    module.validate(config, identity, "https://mcp.example", env)
    state = json.loads((config / "auth-continuity/state.json").read_text())
    state.update(writer_id="haos-executor-id", read_only=False)
    file(config / "auth-continuity/state.json", state)
    module.validate(config, identity, "https://mcp.example", env)
    env["ADMIN_PASSWORD"] = "rotated-after-promotion"
    module.validate(config, identity, "https://mcp.example", env)

@pytest.mark.parametrize("case", ["missing_marker", "not_fenced", "missing_domain", "pending_snapshot", "wrong_writer", "wrong_credential", "missing_db", "unsafe_origin"])
def test_unprepared_or_mismatched_writer_fails_closed(tmp_path, case):
    config, identity, env = fixture(tmp_path)
    origin = "https://mcp.example"
    if case == "missing_marker":
        (config / "single-writer-handoff.json").unlink()
    elif case in ("not_fenced", "missing_domain"):
        marker = json.loads((config / "single-writer-handoff.json").read_text())
        if case == "not_fenced":
            marker["source_writer_fenced"] = False
        else:
            marker["domains"].remove("tasks")
        file(config / "single-writer-handoff.json", marker)
    elif case == "pending_snapshot":
        file(config / "auth-continuity/state.json", {"generation": 12, "pending": True, "slot": 0, "writer_id": "old-writer"})
    elif case == "wrong_writer":
        file(config / "membership/membership.json", {"writer_id": "old-writer", "nodes": {}})
    elif case == "wrong_credential":
        env["MCP_RELAY_AGENT_TOKEN"] = "wrong"
    elif case == "missing_db":
        (config / "tasks_state.sqlite").unlink()
    elif case == "unsafe_origin":
        origin = "http://mcp.example"
    with pytest.raises((ValueError, OSError)):
        module.validate(config, identity, origin, env)

def test_real_entrypoint_writer_branch_validates_and_sets_single_authority(tmp_path):
    import os
    import shutil
    import subprocess

    if not shutil.which("jq"):
        pytest.skip("jq is required")
    config, identity, env = fixture(tmp_path)
    identity_dir = tmp_path / "identity-dir"
    identity_dir.mkdir()
    (identity_dir / "shellmcp_identity.json").write_bytes(identity.read_bytes())
    (identity_dir / "shellmcp_identity.json").chmod(0o600)
    (identity_dir / "shellmcp_ed25519").write_text("test-only")
    opts = tmp_path / "options.json"
    opts.write_text(json.dumps({"node_mode": "writer", "quorum_enabled": False}))
    text = (PATH.parent / "run.sh").read_text()
    start = text.index("NODE_MODE=\"$(jqr '.node_mode')\"")
    end = text.index("\nesac\n", start) + len("\nesac\n")
    branch = text[start:end].replace("/usr/local/bin/gptadmin_single_writer.py", str(PATH))
    branch = branch.replace("/data/config", str(config)).replace("/data/identity", str(identity_dir))
    branch = branch.replace("/data/spool", str(tmp_path / "spool")).replace("/data/outbox", str(tmp_path / "outbox"))
    script = """set -euo pipefail
OPTS=$1
jqr(){ jq -r "$1 // empty" "$OPTS"; }
PUBLIC_ORIGIN=https://mcp.example
""" + branch + """
printf '%s|%s|%s' "$GPTADMIN_AUTH_MODE" "$GPTADMIN_NODE_PUBLIC_URL" "${GPTADMIN_NODE_SEED-unset}"
"""
    process = subprocess.run(["bash", "-c", script, "test", str(opts)], env={**os.environ, **env}, text=True, capture_output=True)
    assert process.returncode == 0, process.stderr
    assert process.stdout == "writer|https://mcp.example|unset"
    opts.write_text(json.dumps({"node_mode": "writer", "quorum_enabled": True}))
    conflict = subprocess.run(["bash", "-c", script, "test", str(opts)], env={**os.environ, **env}, text=True, capture_output=True)
    assert conflict.returncode != 0
    assert "single writer mode cannot enable quorum" in conflict.stderr
    opts.write_text(json.dumps({"node_mode": "writer", "quorum_enabled": False}))
    (config / "single-writer-handoff.json").unlink()
    process = subprocess.run(["bash", "-c", script, "test", str(opts)], env={**os.environ, **env}, text=True, capture_output=True)
    assert process.returncode != 0
    assert "handoff is incomplete or invalid" in process.stderr

def test_public_export_contains_writer_preflight(tmp_path):
    import subprocess

    output = tmp_path / "export"
    subprocess.run(
        [str(ROOT / "scripts/export_haos_app_repository.sh"), "--output", str(output), "--source-ref", "a" * 40],
        cwd=ROOT, check=True, capture_output=True, text=True,
    )
    exported = output / "gptadmin_hub_standby"
    assert (exported / "gptadmin_single_writer.py").read_bytes() == PATH.read_bytes()
    assert "gptadmin_single_writer.py" in (exported / "Dockerfile").read_text()
    assert "gptadmin_single_writer.py" in (output / ".github/workflows/publish-haos-image.yml").read_text()

def test_writer_keeps_proxy_but_does_not_run_promotion_watchdog(tmp_path, monkeypatch):
    import importlib.util
    import signal

    path = PATH.parent / "gptadmin_failover_runtime.py"
    spec = importlib.util.spec_from_file_location("haos_failover_runtime_writer_test", path)
    runtime = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(runtime)
    for filename in ("failover_config.json", "failover_state.json"):
        (tmp_path / filename).write_text("{}")
    monkeypatch.setattr(runtime, "FAILOVER_DIR", tmp_path)
    monkeypatch.setattr(runtime, "load_options", lambda: {"node_mode": "writer"})
    handlers = {}
    monkeypatch.setattr(runtime.signal, "signal", lambda sig, handler: handlers.__setitem__(sig, handler))
    events = []
    class Proxy:
        def poll(self): return None
        def terminate(self): events.append("proxy_stopped")
        def wait(self, timeout): return 0
    monkeypatch.setattr(runtime, "start_proxy", lambda options: events.append("proxy_started") or Proxy())
    monkeypatch.setattr(runtime, "stop_frpc", lambda: events.append("cleanup"))
    monkeypatch.setattr(runtime.subprocess, "run", lambda *a, **k: pytest.fail("promotion watchdog ran"))
    monkeypatch.setattr(runtime.time, "sleep", lambda seconds: handlers[signal.SIGTERM](signal.SIGTERM, None))
    assert runtime.main() == 0
    assert events == ["proxy_started", "cleanup", "proxy_stopped"]
