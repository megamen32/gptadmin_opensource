"""Contract checks for the documented deployment reference architectures."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BLUEPRINTS = ROOT / "docs" / "DEPLOYMENT_BLUEPRINTS.md"


def test_reference_deployments_cover_each_operating_context() -> None:
    """Each reference deployment must include topology, trade-offs and recovery."""

    document = BLUEPRINTS.read_text(encoding="utf-8")
    lower = document.lower()
    for title in ("small-team", "home-lab", "production"):
        assert f"## {title}" in lower
    for required in (
        "topology",
        "security trade-offs",
        "cost trade-offs",
        "incident runbook",
        "verification command",
        "does not prove physical failover",
    ):
        assert required in lower, f"blueprints are missing {required!r}"


def test_reference_deployment_commands_and_runbooks_are_tracked() -> None:
    """Blueprint commands must point at repository-owned, reviewable checks."""

    document = BLUEPRINTS.read_text(encoding="utf-8")
    for relative_path in (
        "tests/e2e/failover/run.sh",
        "docs/FAILOVER.md",
        "docs/BACKUP_RESTORE.md",
        "docs/NETWORK_PROXY.md",
        "docs/SLO_ALERTS.md",
    ):
        assert relative_path in document
        assert (ROOT / relative_path).exists(), relative_path
