"""Contract tests for the release supply-chain policy and gates."""

import re
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]


def _immutable_action_reference(reference: str) -> bool:
    # Local reusable workflows execute from the caller's exact commit. They
    # have no @ref suffix; validate the bounded path instead of accepting all
    # local references or weakening third-party SHA pinning.
    if reference.startswith("./"):
        if not re.fullmatch(r"\./\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml", reference):
            return False
        target = (ROOT / reference).resolve()
        return target.is_file() and target.parent == (ROOT / ".github/workflows").resolve()
    return re.fullmatch(r"[^\s#]+@[0-9a-f]{40}", reference) is not None


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

    action_ref = re.compile(r"uses:\s*([^\s#]+)(?:\s+#.*)?$")
    workflows = sorted((ROOT / ".github" / "workflows").glob("*.y*ml"))
    assert workflows, "workflow inventory is empty"
    mutable: list[str] = []
    for workflow in workflows:
        for line_number, line in enumerate(workflow.read_text(encoding="utf-8").splitlines(), 1):
            if "uses:" not in line:
                continue
            match = action_ref.search(line.strip())
            if match is None or not _immutable_action_reference(match[1]):
                mutable.append(f"{workflow.relative_to(ROOT)}:{line_number}: {line.strip()}")
    assert not mutable, "workflow actions must use full commit SHAs:\n" + "\n".join(mutable)


@pytest.mark.parametrize("reference", [
    "actions/checkout@v4", "actions/checkout@main", "actions/checkout@123abc",
    "./.github/workflows/operator-ci.yml@main", "./.github/workflows/missing.yml",
    "./.github/workflows/../operator-ci.yml", "./scripts/local-action",
    "./.github/workflows/operator-ci.yml@11d5960a326750d5838078e36cf38b85af677262",
])
def test_local_workflow_exception_does_not_allow_mutable_or_invalid_references(reference: str) -> None:
    assert not _immutable_action_reference(reference)


def test_local_workflow_and_pinned_third_party_references_are_immutable() -> None:
    local = "operator-ci.yml" if (ROOT / ".github/workflows/operator-ci.yml").exists() else "public-ci.yml"
    assert _immutable_action_reference("./.github/workflows/" + local)
    assert _immutable_action_reference("actions/checkout@11d5960a326750d5838078e36cf38b85af677262")
