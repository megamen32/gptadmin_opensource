"""Bounded tests for the native GPTAdmin MCP Tasks live-canary path."""
from __future__ import annotations

import importlib.util
import os
import subprocess
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]
PATH = ROOT / "scripts/live_canary_native.py"
SPEC = importlib.util.spec_from_file_location("live_canary_native", PATH)
assert SPEC and SPEC.loader
NATIVE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(NATIVE)


def test_native_canary_one_unexecuted_refusal_then_one_task(monkeypatch):
    monkeypatch.setattr(NATIVE.time, "sleep", lambda _delay: None)
    calls = []
    task_polls = 0

    def rpc(url, bearer, method, params, request_id):
        nonlocal task_polls
        assert url == "https://canary.example/server/shell-test-host/mcp"
        assert bearer == "dummy-bearer"
        calls.append((method, request_id))
        if method == "tools/list":
            return {"tools": [{"name": "shell_exec"}]}
        if method == "tools/call":
            cmd = params["arguments"]["cmd"]
            marker = cmd.split("'")[1]
            if request_id == 2:
                return {"resultType": "complete", "structuredContent": {
                    "status": "failed", "executed": False,
                    "error": {"code": "instructions_required"},
                    "instructions": "Read the current profile instructions first.",
                    "context": "test-context",
                }}
            assert params["_meta"]["io.gptadmin/context"] == "test-context"
            rpc.marker = marker
            return {"resultType": "task", "taskId": "task-correct"}
        if method == "tasks/get":
            assert params["taskId"] == "task-correct"
            task_polls += 1
            if task_polls == 1:
                return {"resultType": "complete", "taskId": "task-correct", "status": "working", "pollIntervalMs": 1000}
            return {"resultType": "complete", "taskId": "task-correct", "status": "completed",
                    "result": {"structuredContent": {"response": {"structuredContent": {
                        "result": {"returncode": 0, "stdout": rpc.marker},
                    }}}}}
        raise AssertionError(method)

    marker = NATIVE.prove_execution("https://canary.example/server/shell-test-host/mcp", "dummy-bearer", rpc)
    assert marker == rpc.marker
    assert calls == [("tools/list", 1), ("tools/call", 2), ("tools/call", 3),
                     ("tasks/get", 4), ("tasks/get", 5)]


def test_no_false_positive_from_context_unavailable():
    calls = []

    def rpc(_url, _bearer, method, _params, _request_id):
        calls.append(method)
        if method == "tools/list":
            return {"tools": [{"name": "shell_exec"}]}
        return {"resultType": "complete", "structuredContent": {
            "status": "failed", "executed": False,
            "error": {"code": "instructions_unavailable"}, "message": "context unavailable",
        }}

    with pytest.raises(NATIVE.CanaryFailure, match="shell output did not match"):
        NATIVE.prove_execution("https://canary.example/server/shell-test-host/mcp", "token", rpc)
    assert calls == ["tools/list", "tools/call"]


def test_pending_submission_is_never_resubmitted():
    calls = []

    def rpc(_url, _bearer, method, _params, _request_id):
        calls.append(method)
        if method == "tools/list":
            return {"tools": [{"name": "shell_exec"}]}
        raise NATIVE.CanaryFailure("delivery unknown")

    with pytest.raises(NATIVE.CanaryFailure, match="delivery unknown"):
        NATIVE.prove_execution("https://canary.example/server/shell-test-host/mcp", "token", rpc)
    assert calls == ["tools/list", "tools/call"]


@pytest.mark.parametrize("value", [
    {"stdout": "context unavailable", "returncode": 0},
    {"stdout": "right", "returncode": 1},
    {"response": {"stdout": "wrong", "returncode": 0}},
])
def test_structured_stdout_is_the_only_success_evidence(value):
    assert NATIVE.exact_stdout(value) != "right"


def test_fixed_target_endpoint_and_transport():
    assert NATIVE.endpoint_for("https://canary.example", "shell:test-host") == (
        "https://canary.example/server/shell-test-host/mcp"
    )
    for origin in ("http://bad.example", "https://canary.example/redirect", "https://canary.example?token=x"):
        with pytest.raises(NATIVE.CanaryFailure):
            NATIVE.endpoint_for(origin, "shell:test-host")


def fake_canary_shell(tmp_path: Path, output: str, exit_code: int):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    (bindir / "sudo").write_text('#!/bin/sh\nexec "$@"\n')
    (bindir / "curl").write_text(
        '#!/bin/sh\n'
        'for item in "$@"; do\n'
        ' case "$item" in\n'
        '  */actions/openapi.yaml) printf \'servers:\\n  - url: "https://canary.example"\\n\'; exit 0;;\n'
        '  */secret-input/livecanary|*/mcp-relay/servers) printf 200; exit 0;;\n'
        ' esac\n'
        'done\nexit 1\n'
    )
    for file in bindir.iterdir():
        file.chmod(0o755)
    envfile = tmp_path / "gptadmin.env"
    envfile.write_text(
        'MCP_ORIGIN=https://canary.example\n'
        'TENANT_ORIGIN=\n'
        'GPTADMIN_CUSTOM_MCP_BEARER=dummy-bearer\n'
    )
    stub = tmp_path / "stub.py"
    stub.write_text(f"import sys\nprint({output!r})\nsys.exit({exit_code})\n")
    notify = tmp_path / "notify.env"
    notify.write_text("")
    env = {
        **os.environ,
        "PATH": str(bindir) + os.pathsep + os.environ["PATH"],
        "GPTADMIN_ENV_FILE": str(envfile),
        "GPTADMIN_NOTIFY_ENV_FILE": str(notify),
        "GPTADMIN_NATIVE_CANARY_HELPER": str(stub),
    }
    return subprocess.run(
        ["bash", str(ROOT / "scripts/live_canary.sh")],
        env=env, capture_output=True, text=True, timeout=12,
    )


def test_bash_canary_never_accepts_context_unavailable(tmp_path):
    run = fake_canary_shell(tmp_path, "context unavailable", 0)
    assert run.returncode == 1, (run.stdout, run.stderr)
    assert "FAIL native-execute" in run.stdout
    assert "live canary: 1 failure" in run.stdout


def test_bash_canary_checks_exit_code_not_just_marker(tmp_path):
    run = fake_canary_shell(tmp_path, "gptadmin-native-canary-" + "a" * 20, 1)
    assert run.returncode == 1, (run.stdout, run.stderr)


def test_bash_canary_only_accepts_successful_fresh_marker(tmp_path):
    run = fake_canary_shell(tmp_path, "gptadmin-native-canary-" + "b" * 20, 0)
    assert run.returncode == 0, (run.stdout, run.stderr)
    assert "PASS native-execute" in run.stdout
