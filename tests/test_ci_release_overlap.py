"""Local CI graph and publication guards; hosted jobs provide runtime evidence."""
import json
from pathlib import Path
import subprocess
import sys

import pytest
import yaml

from tools import publish_full_release as release

ROOT = Path(__file__).resolve().parents[1]
OVERLAY = ROOT / ".gitpublic/public/.github/workflows"
WORKFLOWS = OVERLAY if OVERLAY.is_dir() else ROOT / ".github/workflows"
SOURCE = "a" * 40
RUN = 123
ATTEMPT = 2


def _workflow(name):
    return yaml.safe_load((WORKFLOWS / name).read_text())


def test_release_builds_overlap_ci_and_native_lifecycle_precedes_upload():
    ci = _workflow("public-ci.yml")
    job = ci["jobs"]["release-platform"]
    assert job["needs"] == "node-artifact"
    assert job["permissions"] == {"contents": "read", "actions": "read"}
    assert job["strategy"]["fail-fast"] is False
    pairs = [entry["pair"] for entry in job["strategy"]["matrix"]["include"]]
    assert len(pairs) == 8 and set(pairs) == set(release.CI_RELEASE_PAIRS)
    assert "release-platform" in ci["jobs"]["ci-gate"]["needs"]
    assert set(ci["jobs"]["ci-gate"]["needs"]) == set(ci["jobs"]) - {"ci-gate"}
    assert job["env"]["PUBLIC_SOURCE"] == "${{ github.sha }}"
    assert "needs.preflight" not in json.dumps(job)
    steps = job["steps"]
    checkout = next(step for step in steps if step.get("uses", "").startswith("actions/checkout@"))
    assert checkout["with"]["ref"] == "${{ github.sha }}"
    build = next(step for step in steps if step.get("name") == "Build both editions for this exact source and platform")
    assert 'TAGGED_RELEASE=1 RELEASE_TAG="v$version" RELEASE_COMMIT="$public_sha"' in build["run"]
    assert 'release-platform --platform "$PAIR"' in build["run"]
    lifecycle = next(step for step in steps if step.get("name") == release.CI_RELEASE_LIFECYCLE)
    assert lifecycle["if"] == "matrix.pair == 'linux_amd64'"
    assert lifecycle["run"] == "tests/e2e/systemd/run-lifecycle.sh build/gptadmin-ubuntu-x64-full.tar.gz"
    upload = next(step for step in steps if step.get("uses", "").startswith("actions/upload-artifact@"))
    assert steps.index(build) < steps.index(lifecycle) < steps.index(upload)
    assert upload["with"]["name"] == "gptadmin-full-${{ matrix.pair }}-${{ github.sha }}-${{ github.run_attempt }}"
    assert upload["with"]["compression-level"] == 0
    assert upload["with"]["if-no-files-found"] == "error"


def test_shared_go_cache_has_one_writer_per_os_and_keeps_all_shards():
    job = _workflow("public-ci.yml")["jobs"]["go-unit-tests"]
    assert job["strategy"]["matrix"]["shard"] == [0, 1, 2, 3]
    restore = next(step for step in job["steps"] if step.get("uses", "").startswith("actions/cache/restore@"))
    save = next(step for step in job["steps"] if step.get("uses", "").startswith("actions/cache/save@"))
    assert restore["uses"].split("@")[1] == save["uses"].split("@")[1]
    assert "${{ matrix.shard }}" not in restore["with"]["key"]
    assert "${{ runner.os }}" in restore["with"]["key"] and "${{ runner.arch }}" in restore["with"]["key"]
    assert "tests-1-" in restore["with"]["restore-keys"] and "tests-0-" in restore["with"]["restore-keys"]
    assert "matrix.shard == 0" in save["if"]
    assert save["with"]["key"] == "${{ steps.go-cache.outputs.cache-primary-key }}"
    assert save["continue-on-error"] is True
    assert not any(step.get("uses", "").startswith("actions/cache@") for step in job["steps"])
    run = next(step["run"] for step in job["steps"] if step.get("name") == "Run every test in its deterministic shard")
    assert '--index "$SHARD" --count 4' in run


def test_one_publisher_consumes_exact_ci_attempt_then_publishes_node_and_full():
    workflow = _workflow("publish-node-release.yml")
    manual = _workflow("publish-full-release.yml")
    assert workflow["concurrency"]["group"] == manual["concurrency"]["group"]
    job = workflow["jobs"]["build-test-publish"]
    assert workflow["on"]["workflow_run"]["types"] == ["in_progress"]
    assert "workflow_run.event == 'push'" in job["if"]
    assert "workflow_run.head_branch == 'main'" in job["if"]
    assert job["env"]["CI_RUN_ATTEMPT"] == "${{ github.event.workflow_run.run_attempt }}"
    steps = job["steps"]
    node_download = next(step for step in steps if step.get("name") == "Download exact Node packages tested by Public Product CI")
    assert node_download["with"]["artifact-ids"] == "${{ steps.artifacts.outputs.node_ids }}"
    node = next(step for step in steps if step.get("name") == "Publish immutable Node release for this public source commit")
    full = next(step for step in steps if step.get("name") == "Publish the verified full release without changing latest")
    assert steps.index(node) < steps.index(full)
    assert full["if"] == node["if"]
    assert "steps.scope.outputs.changed == 'true'" in full["if"]
    assert full["env"]["PRIVATE_SOURCE"] == "${{ steps.scope.outputs.private_source }}"
    assert '--ci-artifact-attempt "$CI_RUN_ATTEMPT"' in full["run"]
    download = next(step for step in steps if step.get("name") == "Download complete release shards from the same exact CI attempt")
    assert download["if"] == full["if"]
    assert download["with"]["run-id"] == "${{ github.event.workflow_run.id }}"
    assert download["with"]["merge-multiple"] is False
    assert download["with"]["artifact-ids"] == "${{ steps.artifacts.outputs.full_ids }}"
    gate = next(step for step in steps if "before creating any release tag" in step.get("name", ""))
    assert steps.index(download) < steps.index(gate) < steps.index(node)
    assert "tools/wait_ci_release.py success" in gate["run"]
    assert "verify_ci_release_artifacts" in gate["run"]
    commands = "\n".join(step.get("run", "") for step in steps)
    assert "tools/build.sh" not in commands and "run-lifecycle.sh" not in commands
    assert "verify_ci_release_artifacts" in commands
    assert "release-platform --platform" in json.dumps(manual)
    assert "run-lifecycle.sh" in json.dumps(manual)


@pytest.mark.parametrize("message,valid", [
    ("snapshot\n\nSource-Commit: " + "b" * 40, True),
    ("snapshot", False),
    ("snapshot\n\nSource-Commit: " + "b" * 40 + "\nSource-Commit: " + "c" * 40, False),
    ("snapshot\n\nSource-Commit: " + "b" * 40 + "\nextra trailer", False),
    ("snapshot\n\nSource-Commit: not-a-commit", False),
])
def test_private_source_comes_from_one_final_validated_commit_trailer(tmp_path, message, valid):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                    "commit", "--allow-empty", "-F", "-"], input=message, text=True,
                   cwd=tmp_path, check=True, capture_output=True)
    steps = _workflow("publish-node-release.yml")["jobs"]["build-test-publish"]["steps"]
    source = next(step["run"] for step in steps if step.get("id") == "scope")
    script = source.split('private_source="$(python3 - <<\'PY\'\n', 1)[1].split('\nPY\n)"', 1)[0]
    result = subprocess.run([sys.executable, "-c", script], cwd=tmp_path, capture_output=True, text=True)
    assert (result.returncode == 0) is valid
    if valid:
        assert result.stdout.strip() == "b" * 40


def _ci_state():
    run = {"name": "Public Product CI", "head_sha": SOURCE, "path": ".github/workflows/public-ci.yml",
           "event": "push", "head_branch": "main", "status": "completed",
           "conclusion": "success", "run_attempt": ATTEMPT}
    jobs = [{"name": f"Release bundles ({pair})", "run_id": RUN,
             "status": "completed", "conclusion": "success", "steps": []}
            for pair in release.CI_RELEASE_PAIRS]
    jobs[0]["steps"] = [{"name": release.CI_RELEASE_LIFECYCLE,
                         "status": "completed", "conclusion": "success"}]
    return run, jobs


@pytest.mark.parametrize("fault", [
    None, "old_ci", "source", "attempt", "wrong_workflow", "pull_request",
    "failed_job", "skipped_job", "duplicate_job", "missing_lifecycle", "skipped_lifecycle",
])
def test_release_artifact_guard_requires_this_attempt_all_platforms_and_real_lifecycle(monkeypatch, fault):
    run, jobs = _ci_state()
    if fault == "old_ci":
        jobs.clear()
    elif fault == "source":
        run["head_sha"] = "c" * 40
    elif fault == "attempt":
        run["run_attempt"] += 1
    elif fault == "wrong_workflow":
        run["path"] = ".github/workflows/another.yml"
    elif fault == "pull_request":
        run["event"] = "pull_request"
    elif fault in ("failed_job", "skipped_job"):
        jobs[-1]["conclusion"] = "failure" if fault == "failed_job" else "skipped"
    elif fault == "duplicate_job":
        jobs.append(dict(jobs[-1]))
    elif fault == "missing_lifecycle":
        jobs[0]["steps"].clear()
    elif fault == "skipped_lifecycle":
        jobs[0]["steps"][0]["conclusion"] = "skipped"
    calls = []
    def gh(*args):
        calls.append(args)
        if "/jobs?" in args[1]:
            return json.dumps({"jobs": jobs}).encode()
        return json.dumps(run).encode()
    monkeypatch.setattr(release.node, "gh", gh)
    if fault:
        with pytest.raises(ValueError):
            release.verify_ci_release_artifacts(release.REPOSITORY, SOURCE, RUN, ATTEMPT)
    else:
        release.verify_ci_release_artifacts(release.REPOSITORY, SOURCE, RUN, ATTEMPT)
    assert all("--method" not in call for call in calls)


def test_old_ci_fast_path_fails_before_any_publication_mutation(monkeypatch, tmp_path):
    run, _ = _ci_state()
    calls = []
    def gh(*args):
        calls.append(args)
        return json.dumps({"jobs": []} if "/jobs?" in args[1] else run).encode()
    monkeypatch.setattr(release, "checked_assets", lambda *args: [])
    monkeypatch.setattr(release, "verify_ci_and_node", lambda *args: None)
    monkeypatch.setattr(release.node, "gh", gh)
    with pytest.raises(ValueError, match="exact release job"):
        release.publish(tmp_path, SOURCE, "b" * 40, 1157, RUN, tmp_path,
                        ci_artifact_attempt=ATTEMPT)
    assert all("--method" not in call for call in calls)
