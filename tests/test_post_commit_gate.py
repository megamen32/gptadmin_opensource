"""Regression tests for the commit-scoped background release gate runner."""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from types import ModuleType

import pytest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "post_commit_gate.py"


def _load_runner() -> ModuleType:
    """Load the runner only after reporting a useful missing-feature failure."""
    assert SCRIPT.exists(), "scripts/post_commit_gate.py has not been implemented"
    spec = importlib.util.spec_from_file_location("post_commit_gate", SCRIPT)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def _git(repo: Path, *args: str) -> str:
    """Run Git in a temporary test repository and return stdout."""
    result = subprocess.run(
        ["git", *args],
        cwd=repo,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return result.stdout.strip()


def _make_repo(tmp_path: Path) -> tuple[Path, str]:
    """Create a repository with a versioned committed tree."""
    repo = tmp_path / "repo"
    repo.mkdir()
    _git(repo, "init", "--quiet")
    _git(repo, "config", "user.email", "gate-test@example.invalid")
    _git(repo, "config", "user.name", "Gate Test")
    (repo / "VERSION").write_text("131\n", encoding="utf-8")
    (repo / "sentinel.txt").write_text("committed\n", encoding="utf-8")
    _git(repo, "add", "VERSION", "sentinel.txt")
    _git(repo, "commit", "--quiet", "-m", "test commit")
    return repo, _git(repo, "rev-parse", "HEAD")


def _wait_for_terminal_status(status_file: Path, timeout: float = 10.0) -> dict[str, object]:
    """Wait for a background test run to publish a terminal result."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        payload = json.loads(status_file.read_text(encoding="utf-8"))
        if payload["status"] in {"passed", "failed"}:
            return payload
        time.sleep(0.05)
    raise AssertionError(f"gate did not finish within {timeout}s: {status_file}")


def test_safe_gate_environment_keeps_only_the_python_dependency_base(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Keep declared Python dependencies available without exposing the real home."""
    runner = _load_runner()
    dependency_base = tmp_path / "python-user-base"
    dependency_base.mkdir()
    artifact = tmp_path / "artifact"
    artifact.mkdir()
    monkeypatch.setattr(runner.site, "getuserbase", lambda: str(dependency_base))

    environment = runner._safe_gate_environment(artifact, "a" * 40, "131")

    assert environment["PYTHONUSERBASE"] == str(dependency_base)
    assert environment["HOME"] == str(artifact / "home")


def test_background_run_is_commit_scoped_unique_locked_and_never_pushes(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Run the exact commit once and reject a concurrent run for that commit."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    fixed_now = datetime(2026, 7, 25, 12, 34, 56, 123456, tzinfo=timezone.utc)

    real_git = subprocess.run(
        ["which", "git"], check=True, text=True, stdout=subprocess.PIPE
    ).stdout.strip()
    git_log = tmp_path / "git-calls.log"
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    shim = bin_dir / "git"
    shim.write_text(
        "#!/bin/sh\n"
        "printf '%s\\n' \"$*\" >> \"$GIT_CALL_LOG\"\n"
        f"exec {real_git} \"$@\"\n",
        encoding="utf-8",
    )
    shim.chmod(0o755)
    monkeypatch.setenv("GIT_CALL_LOG", str(git_log))
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")

    gate = runner.Gate(
        name="committed-tree",
        argv=(
            sys.executable,
            "-c",
            (
                "import pathlib,time; time.sleep(0.5); "
                "assert pathlib.Path('sentinel.txt').read_text() == 'committed\\n'"
            ),
        ),
        cwd=".",
    )
    (repo / "sentinel.txt").write_text("dirty and uncommitted\n", encoding="utf-8")

    started = runner.start_gate_run(
        repo=repo,
        revision=commit,
        results_root=results_root,
        gates=(gate,),
        now=fixed_now,
    )
    status_file = Path(started["status_file"])
    initial = json.loads(status_file.read_text(encoding="utf-8"))
    assert initial["status"] == "running"
    assert initial["commit"] == commit
    assert initial["version"] == "131"
    assert status_file.parent.name.startswith(f"{commit[:12]}-v131-20260725T123456.123456Z")

    with pytest.raises(runner.GateAlreadyRunning):
        runner.start_gate_run(
            repo=repo,
            revision=commit,
            results_root=results_root,
            gates=(gate,),
            now=fixed_now,
        )

    final = _wait_for_terminal_status(status_file)
    assert final["status"] == "passed"
    assert final["commands"][0]["status"] == "passed"
    assert Path(started["log_file"]).is_file()
    assert all("push" not in call.split() for call in git_log.read_text(encoding="utf-8").splitlines())

    repeated = runner.start_gate_run(
        repo=repo,
        revision=commit,
        results_root=results_root,
        gates=(gate,),
        now=fixed_now,
    )
    assert Path(repeated["status_file"]).parent != status_file.parent
    assert Path(repeated["status_file"]).parent.name.endswith("-r000002")
    _wait_for_terminal_status(Path(repeated["status_file"]))


def test_atomic_status_and_exact_commit_lookup(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Publish status atomically and never substitute another commit's latest run."""
    runner = _load_runner()
    repo, first_commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    gate = runner.Gate(name="pass", argv=(sys.executable, "-c", "pass"), cwd=".")

    first = runner.start_gate_run(
        repo=repo,
        revision=first_commit,
        results_root=results_root,
        gates=(gate,),
        now=datetime(2026, 7, 25, 13, 0, tzinfo=timezone.utc),
    )
    _wait_for_terminal_status(Path(first["status_file"]))

    (repo / "VERSION").write_text("132\n", encoding="utf-8")
    _git(repo, "add", "VERSION")
    _git(repo, "commit", "--quiet", "-m", "next version")
    second_commit = _git(repo, "rev-parse", "HEAD")
    second = runner.start_gate_run(
        repo=repo,
        revision=second_commit,
        results_root=results_root,
        gates=(gate,),
        now=datetime(2026, 7, 25, 14, 0, tzinfo=timezone.utc),
    )
    _wait_for_terminal_status(Path(second["status_file"]))

    first_result = runner.latest_result_for_commit(repo, results_root, first_commit)
    second_result = runner.latest_result_for_commit(repo, results_root, second_commit)
    assert first_result.path == Path(first["status_file"])
    assert first_result.payload["commit"] == first_commit
    assert second_result.path == Path(second["status_file"])
    assert second_result.payload["commit"] == second_commit
    assert runner.status_exit_code(first_result.payload) == 0

    replace_calls: list[tuple[Path, Path]] = []
    real_replace = runner.os.replace

    def recording_replace(source: Path, destination: Path) -> None:
        replace_calls.append((Path(source), Path(destination)))
        real_replace(source, destination)

    monkeypatch.setattr(runner.os, "replace", recording_replace)
    status_file = tmp_path / "atomic" / "result.json"
    runner.write_status_atomic(status_file, {"status": "running"})
    assert json.loads(status_file.read_text(encoding="utf-8")) == {"status": "running"}
    assert replace_calls[-1][1] == status_file
    assert not replace_calls[-1][0].exists()


def test_failed_gate_is_machine_readable_and_blocks_follow_up(tmp_path: Path) -> None:
    """Expose command failure and a non-zero status result for later push wrappers."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    gate = runner.Gate(name="fail", argv=(sys.executable, "-c", "raise SystemExit(7)"), cwd=".")

    started = runner.start_gate_run(
        repo=repo,
        revision=commit,
        results_root=results_root,
        gates=(gate,),
        now=datetime(2026, 7, 25, 15, 0, tzinfo=timezone.utc),
    )
    final = _wait_for_terminal_status(Path(started["status_file"]))
    assert final["status"] == "failed"
    assert final["commands"][0]["status"] == "failed"
    assert final["commands"][0]["exit_code"] == 7
    assert runner.status_exit_code(final) != 0


def test_status_uses_newest_sequence_when_wall_clock_moves_back(tmp_path: Path) -> None:
    """A failed later run must block release even if its clock is older."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    passed_gate = runner.Gate(name="pass", argv=(sys.executable, "-c", "pass"), cwd=".")
    failed_gate = runner.Gate(
        name="fail", argv=(sys.executable, "-c", "raise SystemExit(9)"), cwd="."
    )

    older = runner.start_gate_run(
        repo=repo,
        revision=commit,
        results_root=results_root,
        gates=(passed_gate,),
        now=datetime(2026, 7, 25, 16, 0, tzinfo=timezone.utc),
    )
    _wait_for_terminal_status(Path(older["status_file"]))
    newer = runner.start_gate_run(
        repo=repo,
        revision=commit,
        results_root=results_root,
        gates=(failed_gate,),
        now=datetime(2026, 7, 25, 15, 0, tzinfo=timezone.utc),
    )
    _wait_for_terminal_status(Path(newer["status_file"]))

    latest = runner.latest_result_for_commit(repo, results_root, commit)
    assert latest.path == Path(newer["status_file"])
    assert latest.payload["status"] == "failed"
    assert latest.payload["sequence"] == 2
    assert runner.status_exit_code(latest.payload) == 1


def test_startup_failure_terminalizes_artifact_and_commands(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A worker launch error cannot leave a releasable-looking running result."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    gate = runner.Gate(name="pass", argv=(sys.executable, "-c", "pass"), cwd=".")

    real_popen = runner.subprocess.Popen

    def fail_start(args: object, *other_args: object, **kwargs: object) -> object:
        if isinstance(args, list) and len(args) > 2 and args[2] == "_worker":
            raise OSError("simulated worker launch failure")
        return real_popen(args, *other_args, **kwargs)

    monkeypatch.setattr(runner.subprocess, "Popen", fail_start)
    with pytest.raises(OSError, match="worker launch"):
        runner.start_gate_run(repo, commit, results_root, gates=(gate,))

    status_file = next(results_root.glob("*/result.json"))
    payload = json.loads(status_file.read_text(encoding="utf-8"))
    assert payload["status"] == "failed"
    assert payload["finished_at"] is not None
    assert payload["commands"][0]["status"] == "skipped"
    assert payload["commands"][0]["finished_at"] is not None


def test_missing_executable_terminalizes_active_command(tmp_path: Path) -> None:
    """A missing gate executable publishes a failed command instead of running forever."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    missing_gate = runner.Gate(
        name="missing-executable",
        argv=("definitely-not-a-gptadmin-test-executable",),
        cwd=".",
    )

    started = runner.start_gate_run(repo, commit, tmp_path / "results", gates=(missing_gate,))
    final = _wait_for_terminal_status(Path(started["status_file"]))
    assert final["status"] == "failed"
    assert final["commands"][0]["status"] == "failed"
    assert final["commands"][0]["finished_at"] is not None
    assert final["commands"][0]["exit_code"] is None


def test_runner_fails_explicitly_without_posix_locking(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The local release helper must not silently claim Windows support."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    monkeypatch.setattr(runner, "fcntl", None)

    with pytest.raises(RuntimeError, match="POSIX"):
        runner.start_gate_run(repo, commit, tmp_path / "results")


def test_new_run_revokes_prior_pass_before_its_artifact_is_published(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Status must not authorize an older pass during publication of a new run."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    gate = runner.Gate(name="pass", argv=(sys.executable, "-c", "pass"), cwd=".")
    first = runner.start_gate_run(repo, commit, results_root, gates=(gate,))
    _wait_for_terminal_status(Path(first["status_file"]))

    observed_exit_codes: list[int] = []
    real_write_status = runner.write_status_atomic

    def observe_artifact_publication(path: Path, payload: dict[str, object]) -> None:
        real_write_status(path, payload)
        if path.name == "result.json" and payload.get("sequence") == 2 and payload.get("status") == "running":
            result = runner.latest_result_for_commit(repo, results_root, commit)
            observed_exit_codes.append(runner.status_exit_code(result.payload))

    monkeypatch.setattr(runner, "write_status_atomic", observe_artifact_publication)
    second = runner.start_gate_run(repo, commit, results_root, gates=(gate,))
    _wait_for_terminal_status(Path(second["status_file"]))

    assert observed_exit_codes == [2]


def test_status_is_nonzero_immediately_after_new_pointer_publication(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The pointer handoff itself must revoke a prior pass before artifact creation."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    gate = runner.Gate(name="pass", argv=(sys.executable, "-c", "pass"), cwd=".")
    first = runner.start_gate_run(repo, commit, results_root, gates=(gate,))
    _wait_for_terminal_status(Path(first["status_file"]))

    observed_exit_codes: list[int] = []

    def observe_pointer_handoff() -> None:
        observed_exit_codes.append(
            runner.main(["--repo", str(repo), "--results-root", str(results_root), "status", commit])
        )

    monkeypatch.setattr(runner, "_after_pointer_published", observe_pointer_handoff, raising=False)
    second = runner.start_gate_run(repo, commit, results_root, gates=(gate,))
    _wait_for_terminal_status(Path(second["status_file"]))

    assert observed_exit_codes == [1]


@pytest.mark.parametrize(
    "corruption", ["pointer_run_id", "payload_status_file", "payload_version", "payload_sequence_bool"]
)
def test_status_rejects_malformed_pointer_and_artifact_identities(
    tmp_path: Path, corruption: str
) -> None:
    """A passed result is unusable when any authorizing identity is inconsistent."""
    runner = _load_runner()
    repo, commit = _make_repo(tmp_path)
    results_root = tmp_path / "results"
    gate = runner.Gate(name="pass", argv=(sys.executable, "-c", "pass"), cwd=".")
    started = runner.start_gate_run(repo, commit, results_root, gates=(gate,))
    status_file = Path(started["status_file"])
    _wait_for_terminal_status(status_file)
    pointer_file = runner._current_result_pointer(results_root, commit)

    if corruption == "pointer_run_id":
        pointer = json.loads(pointer_file.read_text(encoding="utf-8"))
        pointer["run_id"] = "forged-run-id"
        runner.write_status_atomic(pointer_file, pointer)
    else:
        payload = json.loads(status_file.read_text(encoding="utf-8"))
        if corruption == "payload_status_file":
            payload["status_file"] = str(results_root / "other" / "result.json")
        elif corruption == "payload_version":
            payload["version"] = "999"
        else:
            payload["sequence"] = True
        runner.write_status_atomic(status_file, payload)

    with pytest.raises(RuntimeError):
        runner.latest_result_for_commit(repo, results_root, commit)
