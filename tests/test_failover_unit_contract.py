from pathlib import Path
import stat


CLI = Path(__file__).resolve().parents[1] / "cli.py"


def test_systemd_frpc_is_bound_to_the_primary_hub() -> None:
    """A dead Hub must not leave its public FRP proxy owning the hostname."""

    source = CLI.read_text(encoding="utf-8")
    unit_start = source.index('FRPC_UNIT_TPL = """')
    unit_end = source.index('"""', unit_start + len('FRPC_UNIT_TPL = """'))
    unit = source[unit_start:unit_end]

    assert "BindsTo=gptadmin-hub.service" in unit
    assert "After=network-online.target gptadmin-hub.service" in unit


def test_failover_drill_script_is_executable_from_a_clean_clone() -> None:
    """The documented from-scratch failover command must not depend on chmod."""

    script = Path(__file__).resolve().parents[1] / "tests" / "e2e" / "failover" / "run.sh"
    assert script.stat().st_mode & stat.S_IXUSR, f"{script} is not executable"


def test_failover_frpc_fixture_is_executable_from_a_clean_clone() -> None:
    """The watchdog launches the FRP fixture directly, so Git must preserve its mode."""

    fixture = Path(__file__).resolve().parents[1] / "tests" / "e2e" / "failover" / "fake-frpc"
    assert fixture.stat().st_mode & stat.S_IXUSR, f"{fixture} is not executable"
