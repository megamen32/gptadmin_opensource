"""Regression checks for failover harness process isolation."""

from pathlib import Path
import os
import subprocess
import time


ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / "tests" / "e2e" / "failover" / "run.sh"


def test_native_fake_frpc_accepts_container_pid_one_and_cleans_route(tmp_path) -> None:
    """Docker's entrypoint is PID 1; the native fixture must stay alive for it."""
    binary = tmp_path / "fake-frpc"
    subprocess.run(
        ["cc", "-Wall", "-Wextra", "-Werror", "-O2",
         str(RUNNER.parent / "fake-frpc.c"), "-o", str(binary)],
        check=True, timeout=10,
    )
    route = tmp_path / "route"
    proc = subprocess.Popen(
        [str(binary), "-c", str(tmp_path / "frpc.toml")],
        env={**os.environ, "E2E_RUNNER_PID": "1", "E2E_ROUTE_FILE": str(route),
             "E2E_ROUTE_VALUE": "fallback"},
    )
    try:
        deadline = time.monotonic() + 2
        while not route.exists() and proc.poll() is None and time.monotonic() < deadline:
            time.sleep(0.01)
        assert proc.poll() is None, f"fixture rejected PID 1: exit={proc.returncode}"
        assert route.read_text() == "fallback\n"
        proc.terminate()
        assert proc.wait(timeout=2) == 0
        assert not route.exists()
    finally:
        if proc.poll() is None:
            proc.kill()
        proc.wait(timeout=2)


def test_failover_runner_isolates_process_groups_and_default_ports() -> None:
    """An interrupted run must not poison the next run with fixed resources."""

    source = RUNNER.read_text(encoding="utf-8")
    assert 'root=${GPTADMIN_FAILOVER_E2E_ROOT:-/tmp/gptadmin-failover-e2e-$$}' in source
    assert "choose_port_base() {" in source
    assert 'port_base="${GPTADMIN_FAILOVER_E2E_PORT_BASE:-$(choose_port_base)}"' in source
    assert 'E2E_RUNNER_PID=$$ setsid "$@"' in source
    assert 'E2E_RUNNER_PID=$$ E2E_ROUTE_FILE=' in source
    fake_frpc = (ROOT / "tests" / "e2e" / "failover" / "fake-frpc").read_text(encoding="utf-8")
    assert 'runner_pid="${E2E_RUNNER_PID:?E2E_RUNNER_PID is required}"' in fake_frpc
    assert 'while kill -0 "$runner_pid"' in fake_frpc
    assert 'setsid "$@"' in source
    assert 'kill -- -"$pid"' in source
