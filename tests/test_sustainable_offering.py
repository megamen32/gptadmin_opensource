"""Contract checks for the open-core and optional-service boundary."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_open_core_plan_defines_portability_and_non_lock_in_boundary() -> None:
    """The self-hosted core must remain usable without a hosted service."""

    document = (ROOT / "docs" / "OPEN_CORE_PLAN.md").read_text(encoding="utf-8").lower()
    for required in (
        "self-hosted core",
        "agpl",
        "hosted",
        "operational convenience",
        "data portability",
        "no forced lock-in",
        "support",
        "no self-hosted core regression",
    ):
        assert required in document, f"open-core boundary is missing {required!r}"


def test_documentation_map_exposes_open_core_boundary() -> None:
    """The offering boundary must be discoverable from canonical docs."""

    document = (ROOT / "docs" / "DOCUMENTATION_MAP.md").read_text(encoding="utf-8").lower()
    assert "open-core" in document
    assert "open_core_plan.md" in document

