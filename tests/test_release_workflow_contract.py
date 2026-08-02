"""Regression checks for release provenance gates in GitHub Actions."""

import re
from pathlib import Path

import yaml


WORKFLOW = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "build-and-sync.yml"
AUTO_TAG_WORKFLOW = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "auto-tag.yml"
HAOS_WORKFLOW = Path(__file__).resolve().parents[1] / ".github" / "workflows" / "publish-haos-addon.yml"


def test_release_job_verifies_provenance_before_publication() -> None:
    """Keep digest and installer-link checks ahead of any release upload."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    names = [step.get("name", "") for step in steps]
    manifest_step = next(step for step in steps if step.get("name") == "Verify complete release provenance manifest")
    installer_step = next(step for step in steps if step.get("name") == "Verify installer links for shipped targets")

    assert names.index("Verify complete release provenance manifest") < names.index("Mirror source + tag + GitHub Release to public repo")
    assert "python3 tools/verify_release_manifest.py verify" in manifest_step["run"]
    assert "build/manifest.json" in manifest_step["run"]
    assert "build/gptadmin-sbom.spdx.json" in manifest_step["run"]
    assert "python3 tools/verify_installer_links.py" in installer_step["run"]
    assert "--target linux/amd64" in installer_step["run"]
    assert "--target darwin/arm64" in installer_step["run"]
    assert "--android" in installer_step["run"]


def test_release_job_runs_proxyrelay_tests() -> None:
    """Keep the shipped relay implementation inside the release test gate."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    proxy_step = next(step for step in steps if step.get("name") == "Test Go proxyrelay")
    assert "cd go-proxyrelay" in proxy_step["run"]
    assert "go test ./..." in proxy_step["run"]


def test_release_docs_contract_uses_the_uv_test_environment() -> None:
    """Release docs tests must not bypass dependencies installed by uv sync."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    docs_step = next(step for step in steps if step.get("name") == "Docs product contract")

    assert docs_step["run"].startswith("uv run pytest ")


def test_tagged_release_build_preserves_the_tagged_version() -> None:
    """Tag CI must opt into the build mode that does not bump VERSION."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    build_step = next(step for step in steps if step.get("name") == "Build binaries")

    assert "TAGGED_RELEASE=1" in build_step["run"]
    assert "RELEASE_TAG=\"${GITHUB_REF_NAME}\"" in build_step["run"]
    assert 'release_commit="$(git rev-parse "${GITHUB_SHA}^{commit}")"' in build_step["run"]
    assert 'RELEASE_COMMIT="$release_commit"' in build_step["run"]


def test_public_release_reruns_fail_closed_on_identity_mismatch() -> None:
    """Existing public tags and assets must match exactly and are never overwritten."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    publish_step = next(step for step in steps if step.get("name") == "Mirror source + tag + GitHub Release to public repo")
    script = publish_step["run"]

    assert "--clobber" not in script
    assert 'git tag -a "${TAG}" -m "Release ${TAG}" 2>/dev/null || true' not in script
    assert 'git push origin "${TAG}" ||' not in script
    assert 'remote_tag_lines="$(git ls-remote --tags origin' in script
    assert 'remote_tag_commit' in script
    assert 'expected_public_commit' in script
    assert 'if [[ "$remote_tag_commit" != "$expected_public_commit" ]]' in script
    assert 'gh release view "${TAG}" --repo megamen32/gptadmin_opensource --json assets' in script
    assert 'if [[ "$actual_assets" != "$expected_assets" ]]' in script
    assert "sha256sum" in script
    assert script.count("exit 1") >= 2


def test_public_release_preflights_immutable_identity_before_mutating_remote_main() -> None:
    """An identity mismatch must fail before any public branch mutation."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    publish_step = next(step for step in steps if step.get("name") == "Mirror source + tag + GitHub Release to public repo")
    script = publish_step["run"]
    main_push_index = script.index("git push origin HEAD:main")

    assert script.index('if [[ "$remote_tag_commit" != "$expected_public_commit" ]]') < main_push_index
    assert script.index('if [[ "$actual_assets" != "$expected_assets" ]]') < main_push_index
    assert script.index('gh api --include "repos/megamen32/gptadmin_opensource/releases/tags/${TAG}"') < main_push_index
    assert main_push_index < script.index('git push origin "refs/tags/${TAG}:refs/tags/${TAG}"')
    assert main_push_index < script.index('gh release create "${TAG}"')


def test_publication_waits_for_every_platform_and_ui_gate() -> None:
    """The job that can publish must run only after all independent gates pass."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    job = workflow["jobs"]["build-and-release"]

    assert job["needs"] == ["admin-ui-build", "failover-e2e", "macos-build", "windows-shellmcp"]
    assert "always()" not in str(job.get("if", ""))


def test_auto_tag_verifies_the_fetched_remote_tag_commit_before_no_op() -> None:
    """A local tag alone cannot authorize an idempotent release no-op."""

    workflow = yaml.safe_load(AUTO_TAG_WORKFLOW.read_text(encoding="utf-8"))
    maybe_tag = next(step for step in workflow["jobs"]["tag"]["steps"] if step.get("id") == "maybe_tag")
    script = maybe_tag["run"]

    assert 'current_commit="$(git rev-parse "${GITHUB_SHA}^{commit}")"' in script
    assert 'git ls-remote --exit-code --tags origin "refs/tags/${tag}"' in script
    assert 'git fetch --no-tags origin "refs/tags/${tag}"' in script
    assert 'git rev-parse --verify "FETCH_HEAD^{commit}"' in script
    assert 'if [[ "$remote_tag_commit" != "$current_commit" ]]' in script
    assert "already targets the current commit; nothing to do" in script
    assert 'git rev-parse -q --verify "refs/tags/${tag}"' not in script


def test_auto_tag_retries_dispatch_after_a_verified_existing_tag() -> None:
    """A transient dispatch failure must be recoverable without moving the tag."""

    workflow = yaml.safe_load(AUTO_TAG_WORKFLOW.read_text(encoding="utf-8"))
    release = workflow["jobs"]["release"]

    assert "if" not in release


def test_release_job_attests_artifacts_and_scans_dependencies_before_publication() -> None:
    """Require provenance attestation and vulnerability checks before release sync."""

    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    job = workflow["jobs"]["build-and-release"]
    steps = job["steps"]
    names = [step.get("name", "") for step in steps]
    mirror_index = names.index("Mirror source + tag + GitHub Release to public repo")

    assert job["permissions"]["id-token"] == "write"
    assert job["permissions"]["attestations"] == "write"
    vulnerability_step = next(step for step in steps if step.get("name") == "Scan dependencies for known vulnerabilities")
    attestation_step = next(step for step in steps if step.get("name") == "Attest verified release artifacts")
    assert names.index(vulnerability_step["name"]) < mirror_index
    assert names.index(attestation_step["name"]) < mirror_index
    assert "govulncheck" in vulnerability_step["run"]
    assert "go install golang.org/x/vuln/cmd/govulncheck@v1.6.0" in vulnerability_step["run"]
    assert "govulncheck@latest" not in vulnerability_step["run"]
    assert 'GOVULNCHECK="$(go env GOPATH)/bin/govulncheck"' in vulnerability_step["run"]
    for module in ("go-hub", "go-shellmcp", "go-proxyrelay"):
        module_scan = rf"\(\s+cd {module}\s+\"\$GOVULNCHECK\" \./\.\.\.\s+\)"
        assert re.search(module_scan, vulnerability_step["run"]), f"missing module-local scan for {module}"
        assert f"./{module}/..." not in vulnerability_step["run"]
    assert "npm audit" in vulnerability_step["run"]
    assert re.fullmatch(r"actions/attest-build-provenance@[0-9a-f]{40}", attestation_step["uses"])
    assert attestation_step["if"] == "startsWith(github.ref, 'refs/tags/v') && github.event.repository.private == false"
    assert "build/manifest.json" in attestation_step["with"]["subject-path"]
    assert "build/gptadmin-sbom.spdx.json" in attestation_step["with"]["subject-path"]


def test_haos_image_build_emits_sbom_provenance_and_verifies_digest() -> None:
    """Keep the HAOS image path aligned with the archive supply-chain gate."""

    workflow = yaml.safe_load(HAOS_WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["publish"]["steps"]
    build = next(step for step in steps if step.get("name") == "Build and publish ARM64 image")
    verify = next(step for step in steps if step.get("name") == "Verify HAOS image provenance")
    names = [step.get("name", "") for step in steps]
    assert names.index(verify["name"]) < names.index("Export sanitized Apps repository artifact")
    assert "--sbom=true" in build["run"]
    assert "--provenance=mode=max" in build["run"]
    assert "--metadata-file" in build["run"]
    assert "containerimage.digest" in verify["run"]
    assert "docker buildx imagetools inspect" in verify["run"]
