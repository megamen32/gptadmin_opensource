#!/usr/bin/env python3
"""Paired local Hub upgrade with compatible rollback; run as the host owner/root.

prepare does no service changes. apply stops both writers before snapshotting
state. Candidate startup failure uses the compatible fallback and preserves data. rollback preserves current data and restores the tested compatible Hub,
not the old schema-1 binary. Paths must be explicit; no fleet-wide operations.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import time
import urllib.request

SERVICES = ("gptadmin-hub.service", "gptadmin-hub-standby.service")
WATCHERS = ("gptadmin-failover-controller.timer", "gptadmin-frp-watchdog.timer")
ROOT = Path("/opt/gptadmin")
CONFIG = Path("/etc/gptadmin")
UPSTREAM = Path("/etc/nginx/conf.d/gptadmin-hub-upstream.conf")


def command(*args: str) -> None:
    subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)


def atomic_json(path: Path, value: dict) -> None:
    temporary = path.with_suffix(".tmp")
    with temporary.open("w") as handle:
        json.dump(value, handle, ensure_ascii=False, indent=2)
        handle.flush()
        os.fsync(handle.fileno())
    temporary.replace(path)


def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def state_receipt(path: Path) -> dict:
    if not path.exists():
        return {"schema": "legacy-json"}
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
        schema = db.execute("SELECT version FROM task_store_meta WHERE id=1").fetchone()[0]
        integrity = db.execute("PRAGMA integrity_check").fetchone()[0]
        pending = db.execute("SELECT COUNT(*) FROM task_records WHERE kind IN ('shell','relay') AND json_extract(payload,'$.status') IN ('queued','input_required')").fetchone()[0]
        counts = dict(db.execute("SELECT kind,COUNT(*) FROM task_records GROUP BY kind"))
    if integrity != "ok":
        raise RuntimeError("task database integrity check failed")
    return {"schema": schema, "pending": pending, "counts": counts}


def healthy(expected: str) -> dict:
    result = {}
    for port in (9001, 19001):
        last = "not started"
        for _ in range(100):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/version", timeout=2) as response:
                    version = json.load(response)
                if not str(version.get("git_commit", "")).startswith(expected):
                    raise RuntimeError("unexpected running commit")
                result[str(port)] = version
                break
            except Exception as exc:
                last = str(exc)
                time.sleep(0.1)
        else:
            raise RuntimeError(f"Hub {port} did not become healthy: {last}")
    return result


def install_binary(source: Path) -> None:
    destination = ROOT / "bin/gptadmin_hub"
    temporary = destination.with_suffix(".new")
    shutil.copy2(source, temporary)
    temporary.chmod(0o755)
    temporary.replace(destination)


def install_ui(source: Path) -> None:
    destination = ROOT / "public/admin"
    temporary = ROOT / "public/admin-upgrade-staging"
    if temporary.exists():
        raise RuntimeError("leftover UI staging directory; inspect it before retry")
    shutil.copytree(source, temporary)
    old = ROOT / "public/admin-upgrade-previous"
    if old.exists():
        raise RuntimeError("leftover previous UI directory; inspect it before retry")
    if destination.exists():
        destination.rename(old)
    temporary.rename(destination)
    if old.exists():
        shutil.rmtree(old)


def prepare(args: argparse.Namespace) -> None:
    bundle = args.bundle.resolve()
    bundle.mkdir(mode=0o700, parents=True, exist_ok=False)
    try:
        for name, source in (("candidate", args.candidate), ("fallback", args.fallback)):
            if not source.is_file():
                raise ValueError(f"missing {name} binary")
            shutil.copy2(source, bundle / name)
            (bundle / name).chmod(0o700)
        for name, source in (("candidate-ui", args.ui), ("fallback-ui", args.fallback_ui)):
            if not (source / "index.html").is_file():
                raise ValueError(f"missing {name}")
            shutil.copytree(source, bundle / name)
        manifest = {"candidate_commit": args.commit, "fallback_commit": args.fallback_commit,
                    "candidate_sha256": sha(bundle / "candidate"), "fallback_sha256": sha(bundle / "fallback"),
                    "status": "prepared"}
        atomic_json(bundle / "receipt.json", manifest)
    except Exception:
        # Preserve a failed preparation for inspection; never touch the runtime.
        raise
    print(json.dumps({"status": "prepared", "bundle": str(bundle)}))


def transition(bundle: Path, rollback: bool) -> None:
    bundle = bundle.resolve(strict=True)
    receipt = json.loads((bundle / "receipt.json").read_text())
    if rollback and receipt["status"] != "applied":
        raise RuntimeError("rollback requires an applied bundle")
    if not rollback and receipt["status"] != "prepared":
        raise RuntimeError("apply requires a newly prepared bundle")
    for name in ("candidate", "fallback"):
        if sha(bundle / name) != receipt[name + "_sha256"]:
            raise RuntimeError("artifact checksum changed")
    preflight = state_receipt(CONFIG / "tasks_state.sqlite")
    if preflight.get("pending", 0):
        raise RuntimeError("queued/approval tasks exist; do not abandon their creator-local arguments")
    running_watchers = [unit for unit in WATCHERS if subprocess.run(["systemctl", "is-active", "--quiet", unit], check=False).returncode == 0]
    upstream = UPSTREAM.read_bytes()
    backup_created = False
    original_binary = bundle / "before-hub"
    admission_closed = False
    stopped = False
    try:
        if running_watchers:
            command("systemctl", "stop", *running_watchers)
            for timer in running_watchers:
                command("systemctl", "stop", timer.replace(".timer", ".service"))
        # Hold new public calls while validating migration; workers' outboxes
        # retry after the pair returns. Existing in-flight tasks remain durable.
        UPSTREAM.write_text("upstream gptadmin_hub_active { zone gptadmin_hub_active 64k; server 127.0.0.1:1; }\n")
        command("nginx", "-t")
        command("systemctl", "reload", "nginx")
        admission_closed = True
        command("systemctl", "stop", *SERVICES)
        stopped = True
        offline = state_receipt(CONFIG / "tasks_state.sqlite")
        if offline.get("pending", 0):
            raise RuntimeError("a pending task arrived during cutover; restart original pair without migration")
        if not rollback:
            if (bundle / "before-config").exists():
                raise RuntimeError("backup already exists; inspect before retry")
            shutil.copytree(CONFIG, bundle / "before-config")
            shutil.copy2(ROOT / "bin/gptadmin_hub", original_binary)
            shutil.copytree(ROOT / "public/admin", bundle / "before-ui")
            backup_created = True
        else:
            # Keep current data even on rollback. The fallback implements the
            # same task schema and accepted profile modes as the candidate.
            shutil.copytree(CONFIG, bundle / "pre-rollback-config")
        name = "fallback" if rollback else "candidate"
        install_binary(bundle / name)
        install_ui(bundle / (name + "-ui"))
        command("systemctl", "start", *SERVICES)
        stopped = False
        receipt["versions"] = healthy(receipt[name + "_commit"])
        receipt["state"] = state_receipt(CONFIG / "tasks_state.sqlite")
        receipt["status"] = "rolled_back" if rollback else "applied"
        receipt["finished_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        atomic_json(bundle / "receipt.json", receipt)
    except Exception:
        if backup_created and admission_closed:
            command("systemctl", "stop", *SERVICES)
            # A candidate might have accepted a local worker result while
            # public admission was held. Preserve current data, including that
            # result, and use the schema-compatible fallback rather than
            # restoring a stale database snapshot.
            shutil.copytree(CONFIG, bundle / "failed-candidate-config")
            install_binary(bundle / "fallback")
            install_ui(bundle / "fallback-ui")
            command("systemctl", "start", *SERVICES)
            receipt["versions"] = healthy(receipt["fallback_commit"])
            receipt["status"] = "failed_candidate_fell_back"
            receipt["state"] = state_receipt(CONFIG / "tasks_state.sqlite")
            atomic_json(bundle / "receipt.json", receipt)
        elif rollback and admission_closed and (bundle / "pre-rollback-config").exists():
            command("systemctl", "stop", *SERVICES)
            install_binary(bundle / "candidate")
            install_ui(bundle / "candidate-ui")
            command("systemctl", "start", *SERVICES)
            receipt["versions"] = healthy(receipt["candidate_commit"])
            receipt["status"] = "applied"
            atomic_json(bundle / "receipt.json", receipt)
        elif stopped:
            command("systemctl", "start", *SERVICES)
        raise
    finally:
        UPSTREAM.write_bytes(upstream)
        command("nginx", "-t")
        command("systemctl", "reload", "nginx")
        if running_watchers:
            command("systemctl", "start", *running_watchers)
    print(json.dumps({"status": receipt["status"], "versions": receipt["versions"], "state": receipt["state"]}))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "apply", "rollback"))
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--candidate", type=Path)
    parser.add_argument("--fallback", type=Path)
    parser.add_argument("--ui", type=Path)
    parser.add_argument("--fallback-ui", type=Path)
    parser.add_argument("--commit")
    parser.add_argument("--fallback-commit")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("run as root to preserve private state and manage this service pair")
    os.umask(0o077)
    if args.action == "prepare":
        if not all((args.candidate, args.fallback, args.ui, args.fallback_ui, args.commit, args.fallback_commit)):
            parser.error("prepare requires both binaries/UI directories and their exact commits")
        prepare(args)
    else:
        transition(args.bundle, args.action == "rollback")


if __name__ == "__main__":
    main()
