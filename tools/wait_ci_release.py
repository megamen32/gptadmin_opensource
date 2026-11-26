#!/usr/bin/env python3
"""Read-only waits for one trusted CI attempt's artifacts or successful finish."""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from datetime import datetime
import json
from pathlib import Path
import re
import subprocess
import time
from typing import Callable

PUBLIC_REPOSITORY = "megamen32/gptadmin_opensource"
WORKFLOW_PATH = ".github/workflows/public-ci.yml"
FULL_PAIRS = tuple(f"{platform}_{arch}" for platform in
                   ("linux", "darwin", "windows", "android") for arch in ("amd64", "arm64"))
NODE_PAIRS = tuple(f"{platform}-{arch}" for platform in ("linux", "darwin")
                   for arch in ("amd64", "arm64"))


@dataclass(frozen=True)
class Identity:
    repository: str
    repository_id: int
    source: str
    run_id: int
    attempt: int

    def __post_init__(self):
        if self.repository != PUBLIC_REPOSITORY:
            raise ValueError("only the public product repository is authorized")
        if not re.fullmatch(r"[0-9a-f]{40}", self.source):
            raise ValueError("source must be an exact commit SHA")
        if any(type(value) is not int or value <= 0 for value in
               (self.repository_id, self.run_id, self.attempt)):
            raise ValueError("repository, run and attempt IDs must be positive integers")

    def artifact_names(self):
        suffix = f"-{self.source}-{self.attempt}"
        return {
            **{f"gptadmin-node-{pair}{suffix}": "node" for pair in NODE_PAIRS},
            **{f"gptadmin-full-{pair}{suffix}": "full" for pair in FULL_PAIRS},
        }


def github_get(path: str, timeout: float) -> dict:
    """The helper never calls a GitHub mutation endpoint or shell source."""
    result = subprocess.run(["gh", "api", "--method", "GET", path],
                            check=True, capture_output=True, text=True, timeout=timeout)
    value = json.loads(result.stdout)
    if not isinstance(value, dict):
        raise ValueError("GitHub returned a non-object response")
    return value


def verify_completed_release(identity: Identity, version: int, *, fetch: Callable = github_get) -> None:
    """Reconcile an exact already published pair using bounded GETs only."""
    if type(version) is not int or version <= 0:
        raise ValueError("version must be a positive integer")
    node_tag = f"node-v{version}-{identity.source[:12]}"
    full_tag = f"v{version}"
    names = {
        node_tag: {f"gptadmin-node-{pair}.tar.gz{suffix}"
                   for pair in NODE_PAIRS for suffix in ("", ".sha256")},
        full_tag: {f"gptadmin-{platform}-{arch}-{edition}.{extension}"
                   for platform, extension in (("windows", "zip"), ("macos", "tar.gz"),
                                               ("ubuntu", "tar.gz"), ("android", "tar.gz"))
                   for arch in ("x64", "arm64") for edition in ("full", "client")}
                  | {"gptadmin-checksums.txt", "gptadmin-release-matrix.json"},
    }
    prefix = f"repos/{identity.repository}/"
    paths = [f"releases/tags/{tag}" for tag in names]
    paths += [f"git/ref/tags/{tag}" for tag in names]
    with ThreadPoolExecutor(max_workers=4) as pool:
        responses = dict(zip(paths, pool.map(lambda path: fetch(prefix + path, 30), paths)))
    for tag, expected in names.items():
        release = responses[f"releases/tags/{tag}"]
        if (release.get("tag_name") != tag or release.get("target_commitish") != identity.source
                or release.get("draft") is not False or release.get("prerelease") is not False
                or not isinstance(release.get("published_at"), str) or not release["published_at"]):
            raise ValueError("existing release is not published for this exact source")
        ref = responses[f"git/ref/tags/{tag}"]
        # Both publishers create lightweight immutable tags.
        obj = ref.get("object") or {}
        if (ref.get("ref") != f"refs/tags/{tag}" or obj.get("type") != "commit"
                or obj.get("sha") != identity.source):
            raise ValueError("existing release tag does not bind this exact source")
        assets = release.get("assets")
        if (not isinstance(assets, list) or len(assets) != len(expected)
                or not all(isinstance(asset, dict) for asset in assets)
                or {asset.get("name") for asset in assets} != expected):
            raise ValueError("existing release asset set is incomplete or duplicated")
        for asset in assets:
            if (type(asset.get("id")) is not int or asset["id"] <= 0
                    or type(asset.get("size")) is not int or asset["size"] <= 0
                    or asset.get("state") != "uploaded"
                    or not isinstance(asset.get("digest"), str)
                    or not re.fullmatch(r"sha256:[0-9a-f]{64}", asset["digest"])):
                raise ValueError("existing release asset metadata is incomplete")


def validate_run(run: dict, identity: Identity) -> None:
    expected = {"id": identity.run_id, "run_attempt": identity.attempt,
                "head_sha": identity.source, "head_branch": "main", "event": "push",
                "path": WORKFLOW_PATH, "name": "Public Product CI"}
    if any(run.get(key) != value for key, value in expected.items()):
        raise ValueError("CI identity, source or run attempt changed")
    for key in ("repository", "head_repository"):
        repository = run.get(key) or {}
        if (repository.get("id") != identity.repository_id or
                repository.get("full_name") != identity.repository):
            raise ValueError("CI repository identity differs from the publishing repository")
    status, conclusion = run.get("status"), run.get("conclusion")
    if status not in {"queued", "requested", "waiting", "pending", "in_progress", "completed"}:
        raise ValueError("unknown CI status")
    if status == "completed":
        if conclusion != "success":
            raise ValueError(f"CI finished without success: {conclusion}")
    elif conclusion is not None:
        raise ValueError(f"CI has a non-pending conclusion: {conclusion}")


def validate_artifacts(artifacts: list[dict], identity: Identity, now: float) -> dict[str, str] | None:
    expected = identity.artifact_names()
    selected = {}
    for artifact in artifacts:
        name = artifact.get("name", "")
        if not isinstance(name, str) or name not in expected:
            # Other artifacts and previous attempts cannot satisfy this attempt.
            continue
        if name in selected:
            raise ValueError("duplicate release artifact name")
        owner = artifact.get("workflow_run") or {}
        owner_expected = {"id": identity.run_id, "repository_id": identity.repository_id,
                          "head_repository_id": identity.repository_id,
                          "head_branch": "main", "head_sha": identity.source}
        if any(owner.get(key) != value for key, value in owner_expected.items()):
            raise ValueError("artifact belongs to a different run, repository or source")
        artifact_id = artifact.get("id")
        if type(artifact_id) is not int or artifact_id <= 0 or artifact_id in selected.values():
            raise ValueError("invalid or duplicated artifact ID")
        if artifact.get("expired") is not False:
            raise ValueError("release artifact is expired")
        try:
            expiry = datetime.fromisoformat(artifact["expires_at"].replace("Z", "+00:00"))
            if expiry.tzinfo is None or expiry.timestamp() <= now:
                raise ValueError("release artifact has expired")
        except (KeyError, TypeError, AttributeError) as error:
            raise ValueError("release artifact has no valid expiry") from error
        if not isinstance(artifact.get("size_in_bytes"), int) or artifact["size_in_bytes"] <= 0:
            raise ValueError("release artifact is empty")
        selected[name] = artifact_id
    if set(selected) != set(expected):
        return None
    return {kind + "_ids": ",".join(str(selected[name]) for name in sorted(expected)
                                   if expected[name] == kind) for kind in ("node", "full")}


def wait_for(identity: Identity, mode: str, *, fetch: Callable = github_get,
             sleep: Callable = time.sleep, clock: Callable = time.monotonic,
             wall_clock: Callable = time.time, timeout: float = 1200,
             poll_interval: float = 3) -> dict[str, str]:
    if mode not in {"artifacts", "success"}:
        raise ValueError("unknown wait mode")
    if not 0 < timeout <= 1200 or not 0 < poll_interval <= 30:
        raise ValueError("wait timeout or polling interval is outside its bound")
    deadline = clock() + timeout

    def get(path):
        remaining = deadline - clock()
        if remaining <= 0:
            raise TimeoutError("deadline waiting for exact CI attempt")
        value = fetch(path, min(30, remaining))
        if clock() >= deadline:
            raise TimeoutError("deadline waiting for exact CI attempt")
        return value

    prefix = f"repos/{identity.repository}"
    run_path = f"{prefix}/actions/runs/{identity.run_id}"
    while True:
        run = get(run_path)
        validate_run(run, identity)
        main = get(f"{prefix}/git/ref/heads/main")
        if (main.get("ref") != "refs/heads/main" or
                main.get("object", {}).get("type") != "commit" or
                main.get("object", {}).get("sha") != identity.source):
            raise ValueError("public main moved away from the CI source")
        if mode == "success" and run["status"] == "completed":
            return {}
        if mode == "artifacts":
            artifacts, page = [], 1
            while True:
                listing = get(f"{run_path}/artifacts?per_page=100&page={page}")
                items, count = listing.get("artifacts"), listing.get("total_count")
                if (not isinstance(items, list) or type(count) is not int or count < 0 or
                        any(not isinstance(item, dict) for item in items)):
                    raise ValueError("invalid artifact listing")
                artifacts.extend(items)
                if len(artifacts) >= count:
                    break
                if not items:
                    raise ValueError("incomplete artifact listing")
                page += 1
            found = validate_artifacts(artifacts, identity, wall_clock())
            if found is not None:
                # The attempt can change while listing pages. Never export stale IDs.
                validate_run(get(run_path), identity)
                return found
            if run["status"] == "completed":
                raise ValueError("successful CI attempt is missing required release artifacts")
        remaining = deadline - clock()
        if remaining <= 0:
            raise TimeoutError("deadline waiting for exact CI attempt")
        sleep(min(poll_interval, remaining))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("artifacts", "success"))
    parser.add_argument("--repository", default=PUBLIC_REPOSITORY)
    parser.add_argument("--repository-id", type=int, required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--run-id", type=int, required=True)
    parser.add_argument("--attempt", type=int, required=True)
    parser.add_argument("--timeout-seconds", type=float, default=1200)
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()
    identity = Identity(args.repository, args.repository_id, args.source, args.run_id, args.attempt)
    result = wait_for(identity, args.mode, timeout=args.timeout_seconds)
    if args.github_output:
        with args.github_output.open("a") as stream:
            stream.writelines(f"{key}={value}\n" for key, value in result.items())
    print(json.dumps({"run_id": identity.run_id, "attempt": identity.attempt,
                      "mode": args.mode, **result}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
