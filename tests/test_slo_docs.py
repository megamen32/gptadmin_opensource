"""Contract checks for the operator SLO and alert runbook."""

from pathlib import Path


DOC = Path(__file__).resolve().parents[1] / "docs" / "SLO_ALERTS.md"


def test_slo_runbook_defines_machine_checks_targets_and_recovery() -> None:
    """Keep the operator contract actionable and tied to existing probes."""

    text = DOC.read_text(encoding="utf-8")
    required_sections = (
        "## Service-level objectives",
        "## Error budget",
        "## Alert runbook",
        "### Hub unavailable",
        "### Authentication or policy failures",
        "### Relay or durable job failures",
        "### Backup or failover failure",
    )
    for section in required_sections:
        assert section in text

    for signal in (
        "gptadmin doctor --json",
        "/healthz",
        "/admin/api/overview",
        "/admin/api/audit",
    ):
        assert signal in text

    for target in ("99.9%", "99.5%", "99%"):
        assert target in text

    assert "Owner" in text
    assert "Symptom" in text
    assert "Diagnosis" in text
    assert "Recovery" in text
