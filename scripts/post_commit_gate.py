#!/usr/bin/env python3
"""Run release-oriented local checks against one exact commit in background."""

from __future__ import annotations

import argparse
import dataclasses
import json
import os
import re
import shutil
import site
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Mapping, Sequence

if os.name == "posix":
    import fcntl
else:
    fcntl = None


REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_RESULTS_ROOT = REPO_ROOT / "trash" / "logs" / "post-commit-gates"
VERSION_PATTERN = re.compile(r"[0-9]+")


@dataclasses.dataclass(frozen=True, slots=True)
class Gate:
    """Describe one subprocess gate and its checkout-relative working directory."""

    name: str
    argv: tuple[str, ...]
    cwd: str


@dataclasses.dataclass(frozen=True, slots=True)
class GateResult:
    """Pair a persisted result payload with its immutable artifact path."""

    path: Path
    payload: dict[str, object]


class GateAlreadyRunning(RuntimeError):
    """Raised when another process holds the lock for the same commit."""


def _require_posix_release_host() -> None:
    """Fail explicitly because this local helper relies on POSIX fd locking."""
    if fcntl is None:
        raise RuntimeError("post-commit gate runner requires a POSIX local release host")


DEFAULT_GATES = (
    Gate("python-tests", (sys.executable, "-m", "pytest", "tests/", "--ignore=tests/e2e"), "."),
    Gate("go-hub-tests", ("go", "test", "./..."), "go-hub"),
    Gate("go-shellmcp-tests", ("go", "test", "./..."), "go-shellmcp"),
    Gate("go-proxyrelay-tests", ("go", "test", "./..."), "go-proxyrelay"),
    Gate("admin-ui-install", ("npm", "ci"), "admin-ui"),
    Gate("admin-ui-tests", ("npm", "test"), "admin-ui"),
    Gate("admin-ui-lint", ("npm", "run", "lint"), "admin-ui"),
    Gate("admin-ui-build", ("npm", "run", "build", "--", "--base=/admin/"), "admin-ui"),
    Gate("cli-version", (sys.executable, "cli.py", "version"), "."),
    Gate("cli-auto-update-status", (sys.executable, "cli.py", "auto-update", "status"), "."),
)


def _utc_text(value: datetime | None = None) -> str:
    """Return a fixed-width UTC timestamp suitable for identifiers and JSON."""
    current = value or datetime.now(timezone.utc)
    if current.tzinfo is None:
        raise ValueError("timestamp must be timezone-aware")
    return current.astimezone(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")


def _git(repo: Path, *args: str) -> str:
    """Run a read-only or checkout-preparation Git command without a shell."""
    completed = subprocess.run(
        ["git", *args],
        cwd=repo,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return completed.stdout.strip()


def resolve_commit(repo: Path, revision: str) -> str:
    """Resolve a revision to a full commit identifier or raise ValueError."""
    if not revision or revision.startswith("-"):
        raise ValueError("revision must name a committed Git object")
    try:
        commit = _git(repo, "rev-parse", "--verify", f"{revision}^{{commit}}")
    except subprocess.CalledProcessError as exc:
        raise ValueError(f"revision is not a commit: {revision}") from exc
    if not re.fullmatch(r"[0-9a-f]{40,64}", commit):
        raise ValueError(f"Git returned an invalid commit identifier: {commit!r}")
    return commit


def version_at_commit(repo: Path, commit: str) -> str:
    """Read and validate VERSION from the committed tree, never the worktree."""
    try:
        version = _git(repo, "show", f"{commit}:VERSION").strip()
    except subprocess.CalledProcessError as exc:
        raise ValueError(f"commit {commit} does not contain VERSION") from exc
    if VERSION_PATTERN.fullmatch(version) is None:
        raise ValueError(f"commit {commit} contains invalid VERSION {version!r}")
    return version


def write_status_atomic(path: Path, payload: Mapping[str, object]) -> None:
    """Replace a JSON status file atomically so readers never observe partial data."""
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump(payload, output, ensure_ascii=False, indent=2, sort_keys=True)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def _allocate_artifact(
    results_root: Path, commit: str, version: str, sequence: int, now: datetime | None
) -> Path:
    """Create a unique artifact directory containing commit, version, and UTC time."""
    base_name = f"{commit[:12]}-v{version}-{_utc_text(now)}-r{sequence:06d}"
    results_root.mkdir(parents=True, exist_ok=True)
    for counter in range(1000):
        suffix = "" if counter == 0 else f"-{counter:02d}"
        candidate = results_root / f"{base_name}{suffix}"
        try:
            candidate.mkdir()
            return candidate
        except FileExistsError:
            continue
    raise RuntimeError(f"could not allocate a unique artifact for {base_name}")


def _command_payload(gate: Gate) -> dict[str, object]:
    """Build the initial machine-readable state for one command."""
    return {
        "name": gate.name,
        "argv": list(gate.argv),
        "cwd": gate.cwd,
        "status": "pending",
        "exit_code": None,
        "started_at": None,
        "finished_at": None,
    }


def _current_result_pointer(results_root: Path, commit: str) -> Path:
    """Return the atomically replaced pointer for the current commit result."""
    return results_root / ".current" / f"{commit}.json"


def _after_pointer_published() -> None:
    """Provide a narrow synchronization boundary after publication of a new pointer."""


def _next_sequence(pointer_path: Path, commit: str) -> int:
    """Read the previous per-commit sequence without trusting wall-clock time."""
    if not pointer_path.exists():
        return 1
    pointer = _load_status(pointer_path)
    if pointer.get("commit") != commit:
        raise RuntimeError(f"current-result pointer has a different commit: {pointer_path}")
    previous = pointer.get("sequence")
    if not isinstance(previous, int) or isinstance(previous, bool) or previous < 1:
        raise RuntimeError(f"current-result pointer has an invalid sequence: {pointer_path}")
    return previous + 1


def _terminalize_failed(payload: dict[str, object], error: str) -> None:
    """Make a result and every unfinished command terminal after an internal failure."""
    finished_at = _utc_text()
    commands = payload.get("commands")
    if isinstance(commands, list):
        for command in commands:
            if not isinstance(command, dict):
                continue
            if command.get("status") == "running":
                command["status"] = "failed"
                command["exit_code"] = None
                command["finished_at"] = finished_at
            elif command.get("status") == "pending":
                command["status"] = "skipped"
                command["finished_at"] = finished_at
    payload["status"] = "failed"
    payload["finished_at"] = finished_at
    payload["error"] = error


def _safe_gate_environment(artifact: Path, commit: str, version: str) -> dict[str, str]:
    """Create a minimal environment that omits credentials and user config files."""
    allowed = ("PATH", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR")
    environment = {name: os.environ[name] for name in allowed if name in os.environ}
    isolated_home = artifact / "home"
    isolated_home.mkdir(exist_ok=True)
    environment.update(
        {
            "HOME": str(isolated_home),
            # The isolated HOME would otherwise hide project-declared user-site
            # dependencies from the fresh committed checkout.
            "PYTHONUSERBASE": site.getuserbase(),
            "CI": "1",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "NPM_CONFIG_USERCONFIG": os.devnull,
            "POST_COMMIT_GATE_COMMIT": commit,
            "POST_COMMIT_GATE_VERSION": version,
        }
    )
    cache_candidates = {
        "GOCACHE": Path.home() / ".cache" / "go-build",
        "GOMODCACHE": Path.home() / "go" / "pkg" / "mod",
        "NPM_CONFIG_CACHE": Path.home() / ".npm",
    }
    for name, default_path in cache_candidates.items():
        configured = os.environ.get(name)
        if configured:
            environment[name] = configured
        elif default_path.is_dir():
            environment[name] = str(default_path)
    return environment


def start_gate_run(
    repo: Path,
    revision: str,
    results_root: Path = DEFAULT_RESULTS_ROOT,
    gates: Sequence[Gate] = DEFAULT_GATES,
    now: datetime | None = None,
) -> dict[str, object]:
    """Start an exact-commit gate process and return its immutable artifact identity."""
    _require_posix_release_host()
    repository = repo.resolve()
    result_root = results_root.resolve()
    commit = resolve_commit(repository, revision)
    version = version_at_commit(repository, commit)
    lock_root = result_root / ".locks"
    lock_root.mkdir(parents=True, exist_ok=True)
    lock_file = lock_root / f"{commit}.lock"
    lock_descriptor = os.open(lock_file, os.O_RDWR | os.O_CREAT, 0o600)
    payload: dict[str, object] | None = None
    status_file: Path | None = None
    try:
        try:
            fcntl.flock(lock_descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise GateAlreadyRunning(f"a gate is already running for commit {commit}") from exc

        pointer_file = _current_result_pointer(result_root, commit)
        sequence = _next_sequence(pointer_file, commit)
        artifact = _allocate_artifact(result_root, commit, version, sequence, now)
        status_file = artifact / "result.json"
        log_file = artifact / "gate.log"
        started_at = _utc_text(now)
        payload = {
            "schema_version": 1,
            "run_id": artifact.name,
            "status": "running",
            "commit": commit,
            "short_commit": commit[:12],
            "version": version,
            "sequence": sequence,
            "started_at": started_at,
            "finished_at": None,
            "repository": str(repository),
            "status_file": str(status_file),
            "log_file": str(log_file),
            "commands": [_command_payload(gate) for gate in gates],
        }
        write_status_atomic(
            pointer_file,
            {
                "schema_version": 1,
                "commit": commit,
                "sequence": sequence,
                "run_id": artifact.name,
                "version": version,
                "status_file": str(status_file),
                "status": "running",
            },
        )
        _after_pointer_published()
        log_file.touch(mode=0o600)
        write_status_atomic(status_file, payload)
        worker = subprocess.Popen(
            [
                sys.executable,
                str(Path(__file__).resolve()),
                "_worker",
                "--status-file",
                str(status_file),
                "--lock-fd",
                str(lock_descriptor),
            ],
            cwd=repository,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
            pass_fds=(lock_descriptor,),
        )
    except Exception as exc:
        if payload is not None and status_file is not None:
            _terminalize_failed(payload, f"worker startup failed: {type(exc).__name__}")
            write_status_atomic(status_file, payload)
        os.close(lock_descriptor)
        raise
    os.close(lock_descriptor)
    return {
        "pid": worker.pid,
        "run_id": artifact.name,
        "status": "running",
        "commit": commit,
        "version": version,
        "sequence": sequence,
        "status_file": str(status_file),
        "log_file": str(log_file),
    }


def _load_status(path: Path) -> dict[str, object]:
    """Load a status artifact and require a JSON object."""
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"status artifact must contain an object: {path}")
    return payload


def _run_worker(status_file: Path, lock_descriptor: int) -> int:
    """Prepare an isolated commit checkout, run gates, and publish terminal state."""
    payload = _load_status(status_file)
    artifact = status_file.parent
    checkout = artifact / "checkout"
    failed = False
    failure_summary: str | None = None

    try:
        _require_posix_release_host()
        os.fstat(lock_descriptor)
        log_file = Path(str(payload["log_file"]))
        commit = str(payload["commit"])
        version = str(payload["version"])
        repository = Path(str(payload["repository"]))
        commands = payload.get("commands")
        if not isinstance(commands, list):
            raise ValueError("status commands must be a list")
        environment = _safe_gate_environment(artifact, commit, version)
        with log_file.open("a", encoding="utf-8") as log:
            log.write(f"run={payload['run_id']} commit={commit} version={version}\n")
            log.flush()
            clone = subprocess.run(
                ["git", "clone", "--quiet", "--shared", "--no-checkout", str(repository), str(checkout)],
                check=False,
                text=True,
                stdout=log,
                stderr=subprocess.STDOUT,
                env=environment,
            )
            if clone.returncode != 0:
                failed = True
                failure_summary = f"checkout clone failed with exit code {clone.returncode}"
            else:
                checkout_result = subprocess.run(
                    ["git", "checkout", "--quiet", "--detach", commit],
                    cwd=checkout,
                    check=False,
                    text=True,
                    stdout=log,
                    stderr=subprocess.STDOUT,
                    env=environment,
                )
                if checkout_result.returncode != 0:
                    failed = True
                    failure_summary = f"checkout failed with exit code {checkout_result.returncode}"

            for command in commands:
                if not isinstance(command, dict):
                    raise ValueError("each command status must be an object")
                if failed:
                    break
                command["status"] = "running"
                command["started_at"] = _utc_text()
                write_status_atomic(status_file, payload)
                argv = command["argv"]
                relative_cwd = command["cwd"]
                if not isinstance(argv, list) or not all(isinstance(item, str) for item in argv):
                    raise ValueError("command argv must be a string list")
                if not isinstance(relative_cwd, str):
                    raise ValueError("command cwd must be a string")
                command_cwd = (checkout / relative_cwd).resolve()
                if checkout.resolve() not in (command_cwd, *command_cwd.parents):
                    raise ValueError(f"command cwd escapes checkout: {relative_cwd}")
                log.write(f"\n[{command['name']}] {json.dumps(argv)}\n")
                log.flush()
                completed = subprocess.run(
                    argv,
                    cwd=command_cwd,
                    check=False,
                    text=True,
                    stdout=log,
                    stderr=subprocess.STDOUT,
                    env=environment,
                )
                command["exit_code"] = completed.returncode
                command["finished_at"] = _utc_text()
                command["status"] = "passed" if completed.returncode == 0 else "failed"
                write_status_atomic(status_file, payload)
                if completed.returncode != 0:
                    failed = True
                    failure_summary = f"gate {command['name']} failed with exit code {completed.returncode}"
                    break
    except Exception as exc:
        failed = True
        failure_summary = f"runner error: {type(exc).__name__}"
    finally:
        try:
            if checkout.exists():
                try:
                    shutil.rmtree(checkout)
                except OSError as exc:
                    failed = True
                    failure_summary = f"checkout cleanup failed: {type(exc).__name__}"
            if failed:
                _terminalize_failed(payload, failure_summary or "runner failed")
            else:
                payload["status"] = "passed"
                payload["finished_at"] = _utc_text()
            write_status_atomic(status_file, payload)
        finally:
            try:
                os.close(lock_descriptor)
            except OSError:
                pass
    return 1 if failed else 0


def latest_result_for_commit(repo: Path, results_root: Path, revision: str) -> GateResult:
    """Return the pointer-selected result for the exact commit, never a timestamp guess."""
    commit = resolve_commit(repo.resolve(), revision)
    root = results_root.resolve()
    pointer_path = _current_result_pointer(root, commit)
    if not pointer_path.exists():
        raise FileNotFoundError(f"no current gate result exists for commit {commit}")
    pointer = _load_status(pointer_path)
    if pointer.get("commit") != commit:
        raise RuntimeError(f"current-result pointer has a different commit: {pointer_path}")
    expected_version = version_at_commit(repo.resolve(), commit)
    sequence = pointer.get("sequence")
    if not isinstance(sequence, int) or isinstance(sequence, bool) or sequence < 1:
        raise RuntimeError(f"current-result pointer has an invalid sequence: {pointer_path}")
    pointer_run_id = pointer.get("run_id")
    if not isinstance(pointer_run_id, str) or not pointer_run_id:
        raise RuntimeError(f"current-result pointer has an invalid run identifier: {pointer_path}")
    if pointer.get("version") != expected_version:
        raise RuntimeError(f"current-result pointer has a different version: {pointer_path}")
    pointer_status_file = pointer.get("status_file")
    if not isinstance(pointer_status_file, str) or not pointer_status_file:
        raise RuntimeError(f"current-result pointer has an invalid status path: {pointer_path}")
    status_file = Path(pointer_status_file).resolve()
    if status_file.name != "result.json" or status_file.parent.parent != root:
        raise RuntimeError(f"current-result pointer escapes artifact root: {pointer_path}")
    payload = _load_status(status_file)
    artifact_run_id = status_file.parent.name
    if pointer_run_id != artifact_run_id:
        raise RuntimeError(f"current-result pointer run identifier does not match artifact: {pointer_path}")
    payload_sequence = payload.get("sequence")
    if (
        payload.get("commit") != commit
        or not isinstance(payload_sequence, int)
        or isinstance(payload_sequence, bool)
        or payload_sequence != sequence
    ):
        raise RuntimeError(f"current-result pointer does not match its artifact: {pointer_path}")
    if payload.get("run_id") != pointer_run_id:
        raise RuntimeError(f"result artifact run identifier does not match its pointer: {status_file}")
    payload_status_file = payload.get("status_file")
    if not isinstance(payload_status_file, str) or Path(payload_status_file).resolve() != status_file:
        raise RuntimeError(f"result artifact status path does not match its pointer: {status_file}")
    if payload.get("version") != expected_version:
        raise RuntimeError(f"result artifact version does not match commit {commit}: {status_file}")
    return GateResult(path=status_file, payload=payload)


def status_exit_code(payload: Mapping[str, object]) -> int:
    """Return zero only for a passed result so follow-up wrappers fail closed."""
    status = payload.get("status")
    if status == "passed":
        return 0
    if status == "running":
        return 2
    return 1


def _parser() -> argparse.ArgumentParser:
    """Build the command-line parser."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=REPO_ROOT, help="Git repository (default: project root)")
    parser.add_argument(
        "--results-root",
        type=Path,
        default=DEFAULT_RESULTS_ROOT,
        help="artifact root (default: trash/logs/post-commit-gates)",
    )
    subparsers = parser.add_subparsers(dest="action", required=True)
    start = subparsers.add_parser("start", help="start gates for an exact committed revision")
    start.add_argument("revision")
    status = subparsers.add_parser("status", help="show the latest result for an exact committed revision")
    status.add_argument("revision")
    worker = subparsers.add_parser("_worker", help=argparse.SUPPRESS)
    worker.add_argument("--status-file", type=Path, required=True)
    worker.add_argument("--lock-fd", type=int, required=True)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    """Run the requested start, status, or internal worker action."""
    arguments = _parser().parse_args(argv)
    if arguments.action == "_worker":
        return _run_worker(arguments.status_file, arguments.lock_fd)
    if arguments.action == "start":
        try:
            started = start_gate_run(arguments.repo, arguments.revision, arguments.results_root)
        except (GateAlreadyRunning, RuntimeError, ValueError) as exc:
            print(str(exc), file=sys.stderr)
            return 1
        print(json.dumps(started, indent=2, sort_keys=True))
        return 0
    try:
        result = latest_result_for_commit(arguments.repo, arguments.results_root, arguments.revision)
    except (FileNotFoundError, RuntimeError, ValueError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print(json.dumps(result.payload, indent=2, sort_keys=True))
    return status_exit_code(result.payload)


if __name__ == "__main__":
    raise SystemExit(main())
