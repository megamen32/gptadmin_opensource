"""Contract tests for the release supply-chain policy and gates."""

import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_supply_chain_policy_documents_verification_response_and_bypass_boundary() -> None:
    """Keep operator verification, response deadlines and bypass limits explicit."""

    policy = (ROOT / "docs" / "SUPPLY_CHAIN.md").read_text(encoding="utf-8").lower()
    for required in (
        "sha-256",
        "sbom",
        "attest-build-provenance",
        "govulncheck",
        "npm audit",
        "critical",
        "24 hours",
        "seven calendar days",
        "gptadmin_update_skip_manifest=1",
        "diagnostic-only",
    ):
        assert required in policy, f"policy is missing {required!r}"


def test_workflows_pin_third_party_actions_to_immutable_commits() -> None:
    """Release/deployment workflows must not execute mutable action tags."""

    action_ref = re.compile(r"uses:\s*[^\s#]+@([0-9a-f]{40})(?:\s+#.*)?$")
    workflows = sorted((ROOT / ".github" / "workflows").glob("*.y*ml"))
    assert workflows, "workflow inventory is empty"
    mutable: list[str] = []
    for workflow in workflows:
        for line_number, line in enumerate(workflow.read_text(encoding="utf-8").splitlines(), 1):
            if "uses:" not in line:
                continue
            if not action_ref.search(line.strip()):
                mutable.append(f"{workflow.relative_to(ROOT)}:{line_number}: {line.strip()}")
    assert not mutable, "workflow actions must use full commit SHAs:\n" + "\n".join(mutable)
