"""Unit coverage for polling decisions; GitHub API and clock are injected."""
from copy import deepcopy
import math
from pathlib import Path
import subprocess

import pytest
import yaml

from tools import wait_ci_release as wait

IDENTITY = wait.Identity(wait.PUBLIC_REPOSITORY, 1235588595, "a" * 40, 123, 2)
ROOT = Path(__file__).resolve().parents[1]
OVERLAY = ROOT / ".gitpublic/public/.github/workflows"
WORKFLOWS = OVERLAY if OVERLAY.is_dir() else ROOT / ".github/workflows"


def ci_run(status="in_progress", conclusion=None):
    repository = {"id": IDENTITY.repository_id, "full_name": IDENTITY.repository}
    return {"id": IDENTITY.run_id, "run_attempt": IDENTITY.attempt,
            "head_sha": IDENTITY.source, "head_branch": "main", "event": "push",
            "path": wait.WORKFLOW_PATH, "name": "Public Product CI",
            "repository": deepcopy(repository), "head_repository": deepcopy(repository),
            "status": status, "conclusion": conclusion}


def all_artifacts():
    return [{"id": number, "name": name, "expired": False, "size_in_bytes": 100,
             "expires_at": "2100-01-01T00:00:00Z", "workflow_run": {
                 "id": IDENTITY.run_id, "repository_id": IDENTITY.repository_id,
                 "head_repository_id": IDENTITY.repository_id, "head_branch": "main",
                 "head_sha": IDENTITY.source}}
            for number, name in enumerate(IDENTITY.artifact_names(), 100)]


class API:
    def __init__(self):
        self.elapsed = 0
        self.sleeps = []
        self.calls = []
        self.runs_read = 0
        self.run = ci_run()
        self.artifacts = all_artifacts()
        self.main = {"ref": "refs/heads/main", "object": {"type": "commit", "sha": IDENTITY.source}}

    def sleep(self, seconds):
        self.sleeps.append(seconds)
        self.elapsed += seconds

    def fetch(self, path, timeout):
        self.calls.append(path)
        assert 0 < timeout <= 30
        if "/artifacts?" in path:
            page = int(path.rsplit("=", 1)[1])
            return {"total_count": len(self.artifacts),
                    "artifacts": deepcopy(self.artifacts[(page - 1) * 100:page * 100])}
        if path.endswith("/git/ref/heads/main"):
            return deepcopy(self.main)
        self.runs_read += 1
        return deepcopy(self.run)

    def wait(self, mode="artifacts", **kwargs):
        return wait.wait_for(IDENTITY, mode, fetch=self.fetch, sleep=self.sleep,
                             clock=lambda: self.elapsed, wall_clock=lambda: 1000, **kwargs)


def test_ready_artifacts_can_be_read_while_ci_still_runs_and_ignore_unrelated_or_old_attempts():
    api = API()
    old = deepcopy(api.artifacts[0])
    old.update(name=old["name"].rsplit("-", 1)[0] + "-1", expired=True)
    api.artifacts.extend([old, {"name": "diagnostic-log"}, {"name": "gptadmin-node-debug"}])
    result = api.wait()
    assert len(result["node_ids"].split(",")) == 4
    assert len(result["full_ids"].split(",")) == 8
    assert set(result["node_ids"].split(",")) | set(result["full_ids"].split(",")) == {str(i) for i in range(100, 112)}
    assert api.sleeps == []
    assert api.runs_read == 2
    assert api.run["status"] == "in_progress"


def test_polling_waits_for_all_twelve_artifacts_with_no_real_sleep():
    api = API()
    complete = api.artifacts
    api.artifacts = complete[:11]
    original = api.sleep
    def sleep(seconds):
        original(seconds)
        api.artifacts = complete
    api.sleep = sleep
    assert api.wait()["full_ids"]
    assert api.sleeps == [3]


def test_artifact_pagination_does_not_confuse_unrelated_total_count_with_required_count():
    api = API()
    api.artifacts = [{"name": f"diagnostic-{i}"} for i in range(100)] + api.artifacts
    assert api.wait()["node_ids"]
    assert any(path.endswith("&page=2") for path in api.calls)


@pytest.mark.parametrize("path,value", [
    ("id", 124), ("run_attempt", 3), ("head_sha", "b" * 40),
    ("head_branch", "topic"), ("event", "pull_request"),
    ("path", ".github/workflows/other.yml"), ("name", "Other CI"),
    ("repository.id", 456), ("repository.full_name", "fork/project"),
    ("head_repository.id", 456), ("head_repository.full_name", "fork/project"),
])
def test_wrong_ci_identity_fails_closed(path, value):
    api = API()
    target = api.run
    parts = path.split(".")
    for key in parts[:-1]:
        target = target[key]
    target[parts[-1]] = value
    with pytest.raises(ValueError):
        api.wait()
    assert api.sleeps == []


@pytest.mark.parametrize("conclusion", ["failure", "cancelled", "timed_out", "skipped", "neutral", None])
@pytest.mark.parametrize("mode", ["artifacts", "success"])
def test_failed_or_cancelled_ci_cannot_prepare_or_publish(conclusion, mode):
    api = API()
    api.run = ci_run("completed", conclusion)
    with pytest.raises(ValueError, match="without success"):
        api.wait(mode)
    assert api.sleeps == []


def test_main_move_fails_closed_before_download_ids_are_exported():
    api = API()
    api.main["object"]["sha"] = "b" * 40
    with pytest.raises(ValueError, match="main moved"):
        api.wait()


def test_attempt_change_during_artifact_listing_fails_closed():
    api = API()
    original = api.fetch
    def fetch(path, timeout):
        value = original(path, timeout)
        if "/artifacts?" in path:
            api.run["run_attempt"] += 1
        return value
    api.fetch = fetch
    with pytest.raises(ValueError, match="attempt changed"):
        api.wait()


@pytest.mark.parametrize("change", [
    {"expired": True}, {"expired": None}, {"expires_at": "1970-01-01T00:00:00Z"},
    {"expires_at": "2100-01-01T00:00:00"}, {"expires_at": "bad"},
    {"size_in_bytes": 0}, {"id": 0}, {"id": True},
])
def test_unusable_expected_artifact_fails_closed(change):
    api = API()
    api.artifacts[0].update(change)
    with pytest.raises(ValueError):
        api.wait()


@pytest.mark.parametrize("key,value", [
    ("id", 124), ("repository_id", 456), ("head_repository_id", 456),
    ("head_sha", "b" * 40), ("head_branch", "topic"),
])
def test_artifact_owner_must_match_exact_ci_source(key, value):
    api = API()
    api.artifacts[0]["workflow_run"][key] = value
    with pytest.raises(ValueError, match="different run"):
        api.wait()


@pytest.mark.parametrize("duplicate", ["name", "id"])
def test_duplicate_artifacts_are_rejected(duplicate):
    api = API()
    if duplicate == "name":
        api.artifacts.append(deepcopy(api.artifacts[0]))
    else:
        api.artifacts[1]["id"] = api.artifacts[0]["id"]
    with pytest.raises(ValueError, match="duplicat"):
        api.wait()


def test_missing_artifacts_after_success_fail_without_waiting_for_another_attempt():
    api = API()
    api.run = ci_run("completed", "success")
    api.artifacts.pop()
    with pytest.raises(ValueError, match="missing required"):
        api.wait()
    assert api.sleeps == []


def test_deadline_is_bounded_with_injected_clock():
    api = API()
    api.artifacts = []
    with pytest.raises(TimeoutError):
        api.wait(timeout=6)
    assert api.elapsed == 6
    assert api.sleeps == [3, 3]


def test_success_gate_waits_for_completed_success_and_reads_no_artifacts():
    api = API()
    original = api.sleep
    def sleep(seconds):
        original(seconds)
        api.run = ci_run("completed", "success")
    api.sleep = sleep
    assert api.wait("success") == {}
    assert api.sleeps == [3]
    assert not any("artifacts?" in path for path in api.calls)


def test_network_timeout_receives_the_remaining_deadline():
    api = API()
    budgets = []
    original = api.fetch
    def fetch(path, timeout):
        budgets.append(timeout)
        api.elapsed += 1
        return original(path, timeout)
    api.fetch = fetch
    with pytest.raises(TimeoutError):
        api.wait(timeout=2)
    assert budgets == [2, 1]


def test_github_transport_only_uses_explicit_get_with_a_process_timeout(monkeypatch):
    calls = []
    def run(argv, **kwargs):
        calls.append((argv, kwargs))
        return subprocess.CompletedProcess(argv, 0, stdout='{"id": 1}')
    monkeypatch.setattr(wait.subprocess, "run", run)
    assert wait.github_get("repos/example/project/actions/runs/1", 4) == {"id": 1}
    assert calls[0][0] == ["gh", "api", "--method", "GET", "repos/example/project/actions/runs/1"]
    assert calls[0][1]["timeout"] == 4
    assert "shell" not in calls[0][1]


def test_early_preparation_keeps_every_tag_and_publication_after_success_gate():
    workflow = yaml.safe_load((WORKFLOWS / "publish-node-release.yml").read_text())
    assert workflow["on"]["workflow_run"]["types"] == ["requested", "completed"]
    job = workflow["jobs"]["build-test-publish"]
    for guard in ("event == 'push'", "head_branch == 'main'", "head_repository.id == github.repository_id",
                  "repository.id == github.repository_id", "name == 'Public Product CI'"):
        assert guard in job["if"]
    steps = job["steps"]
    gate_index = next(i for i, step in enumerate(steps) if "before creating any release tag" in step.get("name", ""))
    gate = steps[gate_index]["run"]
    assert "tools/wait_ci_release.py success" in gate and "verify_ci_release_artifacts" in gate
    assert '--attempt "$CI_RUN_ATTEMPT"' in gate
    early = steps[:gate_index]
    assert any("darwin_package.py" in step.get("run", "") for step in early)
    assert sum(step.get("uses", "").startswith("actions/download-artifact@") for step in early) == 2
    for step in early:
        command = step.get("run", "")
        assert not any(value in command for value in ("git tag ", "gh release ", "python3 tools/publish_node_release.py", "python3 tools/publish_full_release.py"))
        assert not step.get("uses", "").startswith("actions/upload-artifact@")
    publish = next(step for step in steps if "python3 tools/publish_node_release.py" in step.get("run", ""))
    assert publish["run"].index("wait_ci_release.py success") < publish["run"].index("publish_node_release.py")


def test_initial_ci_wave_leaves_capacity_for_early_publisher():
    jobs = yaml.safe_load((WORKFLOWS / "public-ci.yml").read_text())["jobs"]
    initial = {name: job for name, job in jobs.items() if not job.get("needs")}
    assert set(initial) == {"go-unit-tests", "node-artifact", "failover-e2e",
                            "candidate-client-contract", "macos-release-packages",
                            "public-python", "release-platform-critical"}
    total = 0
    for job in initial.values():
        matrix = job.get("strategy", {}).get("matrix", {})
        total += len(matrix["include"]) if "include" in matrix else math.prod(len(values) for values in matrix.values())
    assert total == 19
    assert total + 1 <= 20
    assert jobs["go-unit-tests-linux"]["needs"] == "macos-release-packages"
    assert jobs["macos-build"]["needs"] == "macos-release-packages"
    assert jobs["release-platform-android"]["needs"] == "node-artifact"
    assert jobs["windows-shellmcp"]["needs"] == ["node-artifact"]
    assert jobs["release-platform"]["needs"] == "release-platform-critical"
    for name in ("admin-ui-build", "installer-e2e-linux", "android-dns"):
        assert jobs[name]["needs"] == ["release-platform-critical"]
    expanded_jobs = 0
    for job in jobs.values():
        matrix = job.get("strategy", {}).get("matrix", {})
        expanded_jobs += len(matrix["include"]) if "include" in matrix else math.prod(len(values) for values in matrix.values())
    assert expanded_jobs == 36

    visiting, visited = set(), set()

    def visit(name):
        assert name in jobs
        assert name not in visiting, f"CI dependency cycle through {name}"
        if name in visited:
            return
        visiting.add(name)
        needs = jobs[name].get("needs", [])
        for dependency in [needs] if isinstance(needs, str) else needs:
            visit(dependency)
        visiting.remove(name)
        visited.add(name)

    for name in jobs:
        visit(name)
    assert visited == set(jobs)
    assert set(jobs["ci-gate"]["needs"]) == set(jobs) - {"ci-gate"}
