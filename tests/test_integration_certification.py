"""Machine-readable support and retry policy for GPTAdmin integrations."""

from __future__ import annotations

import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MATRIX = ROOT / "tests" / "fixtures" / "integration-support-matrix.json"
INTEGRATIONS = ROOT / "docs" / "INTEGRATIONS.md"
CONTROL = ROOT / "docs" / "INTEGRATION_CONTROL.md"


def test_integration_inventory_covers_documented_adapters() -> None:
    """Require every documented adapter to carry support and evidence metadata."""

    inventory = json.loads(MATRIX.read_text(encoding="utf-8"))
    assert {item["id"] for item in inventory} == {
        "openai-action",
        "mcp-remote",
        "oauth-handshake",
        "browser-extension",
    }
    documented = INTEGRATIONS.read_text(encoding="utf-8")
    for item in inventory:
        assert item["adapter"] in documented
        assert item["clients"]
        assert item["version_policy"]
        assert item["smoke_command"]
        assert item["evidence_class"] in {"local", "external_required"}
        assert item["write_retry"] == "idempotency_key_required"


def test_integration_control_forbids_ambiguous_write_retries() -> None:
    """Keep the retry/idempotency contract explicit and reviewable."""

    control = CONTROL.read_text(encoding="utf-8")
    assert "idempotency_key" in control
    assert "must not invent an unbounded retry loop" in control
    assert "external_required" in control
