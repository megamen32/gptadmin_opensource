"""Contract checks for evidence-first product feedback intake."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_issue_templates_capture_reproduction_and_product_signal() -> None:
    """Issues must be actionable without collecting secrets or invented metrics."""

    bug = (ROOT / ".github" / "ISSUE_TEMPLATE" / "bug_report.md").read_text(encoding="utf-8").lower()
    feature = (ROOT / ".github" / "ISSUE_TEMPLATE" / "feature_request.md").read_text(encoding="utf-8").lower()
    for required in ("reproduction", "activation", "support", "incident", "immutable evidence", "redact secrets"):
        assert required in bug, f"bug template is missing {required!r}"
    for required in ("problem", "activation", "support", "incident", "immutable evidence", "no data yet"):
        assert required in feature, f"feature template is missing {required!r}"


def test_feedback_loop_document_marks_missing_data_explicitly() -> None:
    """Quarterly reviews must distinguish observed evidence from no data yet."""

    document = (ROOT / "docs" / "FEEDBACK_LOOP.md").read_text(encoding="utf-8").lower()
    for required in (
        "design partner",
        "quarterly review",
        "activation",
        "retention",
        "support",
        "incident",
        "no_data_yet",
        "immutable evidence",
    ):
        assert required in document, f"feedback loop is missing {required!r}"
