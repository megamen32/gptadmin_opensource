"""Local CI graph and publication guards; hosted jobs provide runtime evidence."""
import json
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace

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
    names = ("release-platform-critical", "release-platform-android", "release-platform")
    groups = [ci["jobs"][name] for name in names]
    assert not groups[0].get("needs")
    assert groups[1]["needs"] == "node-artifact"
    assert groups[2]["needs"] == "release-platform-critical"
    assert [entry["pair"] for entry in groups[0]["strategy"]["matrix"]["include"]] == ["linux_amd64"]
    assert {entry["pair"] for entry in groups[1]["strategy"]["matrix"]["include"]} == {"android_amd64", "android_arm64"}
    pairs = [entry["pair"] for job in groups for entry in job["strategy"]["matrix"]["include"]]
    assert len(pairs) == 8 and set(pairs) == set(release.CI_RELEASE_PAIRS)
    assert set(names) <= set(ci["jobs"]["ci-gate"]["needs"])
    assert set(ci["jobs"]["ci-gate"]["needs"]) == set(ci["jobs"]) - {"ci-gate"}
    job = groups[0]
    profile = {key: value for key, value in job.items() if key not in {"needs", "strategy"}}
    for group in groups:
        assert {key: value for key, value in group.items() if key not in {"needs", "strategy"}} == profile
        assert group["permissions"] == {"contents": "read", "actions": "read"}
        assert group["strategy"]["fail-fast"] is False
        assert group["env"]["PUBLIC_SOURCE"] == "${{ github.sha }}"
        assert group["name"] == "Release bundles (${{ matrix.pair }})"
        assert "needs.preflight" not in json.dumps(group)
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
    jobs = _workflow("public-ci.yml")["jobs"]
    groups = [jobs[name] for name in ("go-unit-tests", "go-unit-tests-linux")]
    assert not groups[0].get("needs")
    assert groups[1]["needs"] == "macos-release-packages"
    cases = [(os, shard) for group in groups
             for os in group["strategy"]["matrix"]["os"]
             for shard in group["strategy"]["matrix"]["shard"]]
    assert len(cases) == 8
    assert set(cases) == {(os, shard) for os in ("ubuntu-latest", "macos-latest") for shard in range(4)}
    job = groups[0]
    profile = {key: value for key, value in job.items() if key not in {"needs", "strategy"}}
    for group in groups:
        assert {key: value for key, value in group.items() if key not in {"needs", "strategy"}} == profile
        assert group["strategy"]["matrix"]["shard"] == [0, 1, 2, 3]
        assert group["strategy"]["fail-fast"] is False
        assert group["name"] == "Go source regressions (${{ matrix.os }}, shard ${{ matrix.shard }})"
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
    job = workflow["jobs"]["build-test-publish"]
    assert "concurrency" not in workflow
    assert job["concurrency"] == manual["concurrency"]
    assert job["concurrency"]["cancel-in-progress"] is False
    assert workflow["on"]["workflow_run"]["types"] == ["requested", "completed"]
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


@pytest.mark.parametrize("action,attempt,conclusion,expected", [
    ("requested", 1, None, True),
    ("requested", 2, None, False),
    ("in_progress", 1, None, False),
    ("in_progress", 2, None, False),
    ("completed", 1, "success", False),
    ("completed", 2, "success", True),
    ("completed", 3, "success", True),
    ("completed", 2, "failure", False),
    ("completed", 2, "cancelled", False),
])
@pytest.mark.parametrize("foreign", [False, True])
def test_publisher_event_gate_skips_repeated_stage_events_and_accepts_successful_retries(
        action, attempt, conclusion, expected, foreign):
    job = _workflow("publish-node-release.yml")["jobs"]["build-test-publish"]
    repository = SimpleNamespace(id=123, full_name=release.REPOSITORY)
    run = SimpleNamespace(run_attempt=attempt, conclusion=conclusion,
                          event="pull_request" if foreign else "push", head_branch="main",
                          name="Public Product CI", repository=repository, head_repository=repository)
    github = SimpleNamespace(event=SimpleNamespace(action=action, workflow_run=run),
                             repository_id=123, repository=release.REPOSITORY)
    condition = job["if"].replace("&&", " and ").replace("||", " or ").replace("\n", " ")
    assert eval(condition, {"__builtins__": {}}, {"github": github}) is (expected and not foreign)


def _published_pair_api(version=1161):
    node_tag = f"node-v{version}-{SOURCE[:12]}"
    full_tag = f"v{version}"
    node_names = {f"gptadmin-node-{platform}-{arch}.tar.gz{suffix}"
                  for platform in ("linux", "darwin") for arch in ("amd64", "arm64")
                  for suffix in ("", ".sha256")}
    full_names = {f"gptadmin-{platform}-{arch}-{edition}.{extension}"
                  for platform, extension in (("windows", "zip"), ("macos", "tar.gz"),
                                              ("ubuntu", "tar.gz"), ("android", "tar.gz"))
                  for arch in ("x64", "arm64") for edition in ("full", "client")}
    full_names |= {"gptadmin-checksums.txt", "gptadmin-release-matrix.json"}
    responses = {}
    for tag, names in ((node_tag, node_names), (full_tag, full_names)):
        prefix = f"repos/{release.REPOSITORY}/"
        responses[prefix + "releases/tags/" + tag] = {
            "tag_name": tag, "target_commitish": SOURCE, "draft": False, "prerelease": False,
            "published_at": "2026-11-26T03:00:00Z",
            "assets": [{"id": i, "name": name, "size": 123, "state": "uploaded",
                        "digest": "sha256:" + "c" * 64}
                       for i, name in enumerate(sorted(names), 1)]}
        responses[prefix + "git/ref/tags/" + tag] = {
            "ref": "refs/tags/" + tag, "object": {"type": "commit", "sha": SOURCE}}
    return responses


@pytest.mark.parametrize("tag", ["node-v1161-" + SOURCE[:12], "v1161"])
@pytest.mark.parametrize("fault", [None, "source", "tag_source", "tag_name", "draft", "prerelease",
                                  "unpublished", "missing_asset", "duplicate_asset", "digest",
                                  "asset_state", "asset_size"])
def test_exact_completed_release_skip_requires_both_complete_immutable_releases(tag, fault):
    from tools.wait_ci_release import Identity, verify_completed_release
    identity = Identity(release.REPOSITORY, 123, SOURCE, RUN, ATTEMPT)
    responses = _published_pair_api()
    prefix = f"repos/{release.REPOSITORY}/"
    existing = responses[prefix + "releases/tags/" + tag]
    ref = responses[prefix + "git/ref/tags/" + tag]
    if fault == "source":
        existing["target_commitish"] = "b" * 40
    elif fault == "tag_source":
        ref["object"]["sha"] = "b" * 40
    elif fault == "tag_name":
        ref["ref"] = "refs/tags/another"
    elif fault in {"draft", "prerelease"}:
        existing[fault] = True
    elif fault == "unpublished":
        existing["published_at"] = None
    elif fault == "missing_asset":
        existing["assets"].pop()
    elif fault == "duplicate_asset":
        existing["assets"][-1] = existing["assets"][0]
    elif fault == "digest":
        existing["assets"][0]["digest"] = None
    elif fault == "asset_state":
        existing["assets"][0]["state"] = "new"
    elif fault == "asset_size":
        existing["assets"][0]["size"] = 0
    calls = []
    def fetch(path, timeout):
        assert timeout == 30
        calls.append(path)
        return responses[path]
    if fault:
        with pytest.raises(ValueError):
            verify_completed_release(identity, 1161, fetch=fetch)
    else:
        verify_completed_release(identity, 1161, fetch=fetch)
    assert len(calls) == 4 and set(calls) == set(responses)


@pytest.mark.parametrize("fault", [None, "source", "missing_asset", "draft", "missing_full"])
def test_actual_scope_script_skips_only_the_exact_complete_duplicate(tmp_path, fault):
    script = next(step["run"] for step in
                  _workflow("publish-node-release.yml")["jobs"]["build-test-publish"]["steps"]
                  if step.get("id") == "scope")
    responses = _published_pair_api()
    full_key = f"repos/{release.REPOSITORY}/releases/tags/v1161"
    if fault == "source":
        responses[full_key]["target_commitish"] = "b" * 40
    elif fault == "missing_asset":
        responses[full_key]["assets"].pop()
    elif fault == "draft":
        responses[full_key]["draft"] = True
    elif fault == "missing_full":
        responses.pop(full_key)
    (tmp_path / "api.json").write_text(json.dumps(responses))
    (tmp_path / "VERSION").write_text("1161\n")
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    git = bin_dir / "git"
    git.write_text(f"#!{sys.executable}\nimport sys\n"
                   "if sys.argv[1] == 'log': print('snapshot\\n\\nSource-Commit: ' + 'b' * 40)\n"
                   "elif sys.argv[1] == 'diff-tree': print('VERSION')\n"
                   "else: raise SystemExit(91)\n")
    git.chmod(0o755)
    gh = bin_dir / "gh"
    gh.write_text(f"#!{sys.executable}\nimport json, pathlib, sys\n"
                  "args=sys.argv[1:]\n"
                  "with pathlib.Path('calls.jsonl').open('a') as f: f.write(json.dumps(args)+'\\n')\n"
                  "if args[0] == 'api' and '--jq' in args:\n"
                  f" print('node-v1161-{SOURCE[:12]}')\n"
                  "elif args[:3] == ['api', '--method', 'GET']:\n"
                  " data=json.loads(pathlib.Path('api.json').read_text())\n"
                  " if args[3] not in data: raise SystemExit(22)\n"
                  " print(json.dumps(data[args[3]]))\n"
                  "else: raise SystemExit(92)\n")
    gh.chmod(0o755)
    output = tmp_path / "outputs"
    env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ["PATH"],
               PYTHONPATH=str(ROOT), GITHUB_REPOSITORY=release.REPOSITORY, SOURCE_SHA=SOURCE,
               CI_REPOSITORY_ID="123", CI_RUN_ID=str(RUN), CI_RUN_ATTEMPT=str(ATTEMPT),
               GITHUB_OUTPUT=str(output))
    result = subprocess.run(["bash", "-c", script], cwd=tmp_path, env=env,
                            capture_output=True, text=True, timeout=10)
    assert (result.returncode == 0) is (fault is None), result.stderr
    values = output.read_text()
    if fault is None:
        assert "changed=false\n" in values and "published=true\n" in values
    else:
        assert "bump VERSION" in result.stderr
        assert "changed=false\n" not in values
    calls = [json.loads(line) for line in (tmp_path / "calls.jsonl").read_text().splitlines()]
    assert len(calls) == 5
    assert all("--jq" in args or args[:3] == ["api", "--method", "GET"] for args in calls)
