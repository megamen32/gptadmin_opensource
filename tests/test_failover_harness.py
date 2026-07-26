"""Regression checks for failover harness process isolation."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / "tests" / "e2e" / "failover" / "run.sh"


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
