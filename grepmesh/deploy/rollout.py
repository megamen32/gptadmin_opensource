#!/usr/bin/env python3
"""Preview-bound GrepMesh install/verify/rollback helper.

The manifest contains no token. Apply refuses to run until the operator has
provisioned the existing peer token and transport/firewall gate on both hosts.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shlex
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


class RolloutError(RuntimeError):
    pass


def load_manifest(path: Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if data.get("schema_version") != 1:
        raise RolloutError("unsupported manifest schema")
    if len(data.get("targets", [])) < 2:
        raise RolloutError("the mesh manifest must contain at least two targets")
    return data


def repo_root(manifest: dict[str, Any]) -> Path:
    return Path(manifest["source"]["repository"]).resolve()


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def current_revision(root: Path) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def source_revision_is_ancestor(root: Path, revision: str) -> bool:
    result = subprocess.run(
        ["git", "-C", str(root), "merge-base", "--is-ancestor", revision, "HEAD"],
        capture_output=True,
        text=True,
    )
    return result.returncode == 0


def source_tree_matches(root: Path, revision: str, paths: list[str]) -> bool:
    result = subprocess.run(
        ["git", "-C", str(root), "diff", "--quiet", revision, "--", *paths],
        capture_output=True,
        text=True,
    )
    if result.returncode in (0, 1):
        return result.returncode == 0
    raise RolloutError(result.stderr.strip() or "cannot compare source tree to manifest revision")


def artifact_path(manifest: dict[str, Any]) -> Path:
    return repo_root(manifest) / manifest["source"]["artifact"]


def canonical_sha256(value: Any) -> str:
    raw = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(raw).hexdigest()


def confirmation_payload(manifest: dict[str, Any], digest: str | None) -> dict[str, Any]:
    unit = repo_root(manifest) / manifest["service"]["unit"]
    return {
        "manifest_sha256": canonical_sha256(manifest),
        "rollout_helper_sha256": sha256(Path(__file__).resolve()),
        "revision": manifest["source"]["revision"],
        "artifact": manifest["source"]["artifact"],
        "sha256": digest,
        "unit_sha256": sha256(unit) if unit.exists() else None,
        "targets": [
            {
                "host_id": target["host_id"],
                "ssh_alias": target["ssh_alias"],
                "bind": target["bind"],
                "local_bind": target["local_bind"],
                "config_sha256": canonical_sha256(target_config(manifest, target)),
            }
            for target in manifest["targets"]
        ],
    }


def confirmation(payload: dict[str, Any]) -> str:
    raw = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    return "GREPMESH-" + hashlib.sha256(raw).hexdigest()[:24]


def rollback_confirmation(manifest: dict[str, Any]) -> str:
    payload = {
        "action": "rollback",
        "revision": manifest["source"]["revision"],
        "unit_name": manifest["service"]["unit_name"],
        "targets": [target["host_id"] for target in manifest["targets"]],
    }
    raw = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    return "GREPMESH-ROLLBACK-" + hashlib.sha256(raw).hexdigest()[:24]


def preview(manifest: dict[str, Any]) -> dict[str, Any]:
    root = repo_root(manifest)
    artifact = artifact_path(manifest)
    digest = sha256(artifact) if artifact.exists() else None
    expected = manifest["source"]["revision"]
    actual = current_revision(root)
    source_paths = manifest["source"]["source_paths"]
    payload = confirmation_payload(manifest, digest)
    return {
        "schema_version": 1,
        "current_revision": actual,
        "expected_revision": expected,
        "source_revision_ancestor": source_revision_is_ancestor(root, expected),
        "source_tree_matches_revision": source_tree_matches(root, expected, source_paths),
        "artifact": str(artifact),
        "artifact_exists": artifact.exists(),
        "artifact_sha256": digest,
        "manifest_sha256": canonical_sha256(manifest),
        "rollout_helper_sha256": sha256(Path(__file__).resolve()),
        "unit_sha256": sha256(repo_root(manifest) / manifest["service"]["unit"])
        if (repo_root(manifest) / manifest["service"]["unit"]).exists()
        else None,
        "transport_gate": manifest["transport"],
        "targets": [
            {
                "host_id": target["host_id"],
                "target": target["ssh_alias"],
                "actions": [
                    "create service user/group if absent",
                    "install binary/config/unit with per-apply rollback backup and receipt",
                    "daemon-reload and enable --now",
                    "verify listener, health, digest and service state",
                ],
            }
            for target in manifest["targets"]
        ],
        "confirmation": confirmation(payload),
        "rollback_confirmation": rollback_confirmation(manifest),
    }


def target_command(target: dict[str, Any], script: str) -> subprocess.CompletedProcess[str]:
    alias = target["ssh_alias"]
    command = ["bash", "-s"] if alias == "local" else ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", alias, "bash", "-s"]
    return subprocess.run(command, input=script, text=True, capture_output=True)


def scp_to_target(target: dict[str, Any], source: Path, remote: str) -> None:
    if target["ssh_alias"] == "local":
        Path(remote).write_bytes(source.read_bytes())
        return
    result = subprocess.run(
        ["scp", "-q", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", str(source), f"{target['ssh_alias']}:{remote}"],
        capture_output=True,
        text=True,
    )
    if result.returncode:
        raise RolloutError(result.stderr.strip() or "scp failed")


def target_config(manifest: dict[str, Any], target: dict[str, Any]) -> dict[str, Any]:
    excludes = [
        "**/.git/**", "**/.svn/**", "**/.hg/**", "**/node_modules/**",
        "**/.pnpm-store/**", "**/bower_components/**", "**/venv/**", "**/.venv/**",
        "**/__pycache__/**", "**/.tox/**", "**/.nox/**", "**/.pytest_cache/**",
        "**/.mypy_cache/**", "**/.ruff_cache/**", "**/target/**", "**/dist/**",
        "**/build/**", "**/out/**", "**/.next/**", "**/.nuxt/**", "**/coverage/**",
        "**/.cache/**", "**/.cargo/registry/**", "**/.cargo/git/**", "**/.rustup/**",
        "**/go/pkg/mod/**", "**/.local/share/Trash/**", "**/diag-live/**",
        "**/rollback-*/**", "**/rollback-*", "**/.ssh/**", "**/.gnupg/**",
        "**/.aws/credentials", "**/.netrc", "**/id_rsa", "**/id_ed25519",
        "**/*.pem", "**/*.key", "**/shadow", "**/gshadow",
    ]
    return {
        "host_id": target["host_id"],
        "bind": target["bind"],
        "local_bind": target["local_bind"],
        "root": target["root"],
        "roots": target["roots"],
        "peers": target["peers"],
        "exclude_globs": excludes,
        "peer_auth_token_env": manifest["service"]["peer_token_env"],
        "topology_cache_path": f"{manifest['service']['state_dir']}/topology.json",
    }


def apply(manifest: dict[str, Any], supplied_confirmation: str) -> None:
    if manifest["transport"]["mode"] != "management-vpn":
        raise RolloutError("transport mode is not an approved management-vpn gate")
    if not manifest["transport"]["requires_existing_peer_token"]:
        raise RolloutError("manifest must require a pre-provisioned peer token")
    artifact = artifact_path(manifest)
    if not artifact.exists():
        raise RolloutError(f"missing artifact: {artifact}")
    root = repo_root(manifest)
    revision = manifest["source"]["revision"]
    if not source_revision_is_ancestor(root, revision):
        raise RolloutError("manifest source revision is not an ancestor of current HEAD")
    if not source_tree_matches(root, revision, manifest["source"]["source_paths"]):
        raise RolloutError("GrepMesh binary source differs from the manifest source revision")
    digest = sha256(artifact)
    expected = confirmation(confirmation_payload(manifest, digest))
    if supplied_confirmation != expected:
        raise RolloutError("confirmation does not match the current preview")
    unit = repo_root(manifest) / manifest["service"]["unit"]
    if not unit.exists():
        raise RolloutError(f"missing unit template: {unit}")
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    with tempfile.TemporaryDirectory(prefix="grepmesh-rollout-") as temp:
        temp_root = Path(temp)
        for target in manifest["targets"]:
            remote_artifact = f"/tmp/grepmesh-mcp.{stamp}"
            remote_config = f"/tmp/grepmesh-config.{stamp}.json"
            remote_unit = f"/tmp/grepmesh-unit.{stamp}"
            scp_to_target(target, artifact, remote_artifact)
            config_file = temp_root / f"{target['host_id']}.json"
            config_file.write_text(json.dumps(target_config(manifest, target), indent=2) + "\n", encoding="utf-8")
            scp_to_target(target, config_file, remote_config)
            scp_to_target(target, unit, remote_unit)
            q = manifest["service"]
            backup_dir = f"{q['state_dir']}/rollback-{stamp}"
            script = f"""set -eu
sudo -n true
if ! getent group {shlex.quote(q['service_group'])} >/dev/null; then sudo -n groupadd --system {shlex.quote(q['service_group'])}; fi
getent group {shlex.quote(q['supplementary_group'])} >/dev/null
if ! id -u {shlex.quote(q['service_user'])} >/dev/null 2>&1; then sudo -n useradd --system --gid {shlex.quote(q['service_group'])} --home-dir /nonexistent --shell /usr/sbin/nologin {shlex.quote(q['service_user'])}; fi
sudo -n test -s {shlex.quote(q['peer_env_path'])}
sudo -n install -d -o {shlex.quote(q['service_user'])} -g {shlex.quote(q['service_group'])} -m 0750 {shlex.quote(q['state_dir'])}
sudo -n install -d -o root -g root -m 0755 /etc/grepmesh-mcp
for search_path in {' '.join(shlex.quote(path) for path in q.get('search_acl_paths', []))}; do
  if test -e "$search_path"; then
    sudo -n setfacl -R -m u:{shlex.quote(q['service_user'])}:rX "$search_path"
  fi
done
sudo -n install -d -o root -g root -m 0750 {shlex.quote(backup_dir)}
previous_binary=0
previous_config=0
previous_unit=0
if sudo -n test -e {shlex.quote(manifest['source']['install_path'])}; then sudo -n cp -a {shlex.quote(manifest['source']['install_path'])} {shlex.quote(backup_dir)}/binary; previous_binary=1; fi
if sudo -n test -e {shlex.quote(q['config_path'])}; then sudo -n cp -a {shlex.quote(q['config_path'])} {shlex.quote(backup_dir)}/config.json; previous_config=1; fi
if sudo -n test -e /etc/systemd/system/{shlex.quote(q['unit_name'])}; then sudo -n cp -a /etc/systemd/system/{shlex.quote(q['unit_name'])} {shlex.quote(backup_dir)}/unit; previous_unit=1; fi
sudo -n install -o root -g root -m 0755 {shlex.quote(remote_artifact)} {shlex.quote(manifest['source']['install_path'])}
sudo -n install -o root -g root -m 0644 {shlex.quote(remote_config)} {shlex.quote(q['config_path'])}
sudo -n install -o root -g root -m 0644 {shlex.quote(remote_unit)} /etc/systemd/system/{shlex.quote(q['unit_name'])}
sudo -n systemctl daemon-reload
sudo -n systemctl enable {shlex.quote(q['unit_name'])}
sudo -n systemctl restart {shlex.quote(q['unit_name'])}
sudo -n sha256sum {shlex.quote(manifest['source']['install_path'])}
sudo -n systemctl is-active {shlex.quote(q['unit_name'])}
printf '__GREPMESH_BACKUP__|%s|%s|%s|%s\\n' {shlex.quote(backup_dir)} "$previous_binary" "$previous_config" "$previous_unit"
"""
            result = target_command(target, script)
            if result.returncode:
                raise RolloutError(f"{target['host_id']} apply failed: {result.stderr.strip() or result.stdout.strip()}")
            marker = next(
                (line for line in result.stdout.splitlines() if line.startswith("__GREPMESH_BACKUP__|")),
                None,
            )
            if marker is None:
                raise RolloutError(f"{target['host_id']} apply did not return rollback marker")
            marker_parts = marker.split("|")
            if len(marker_parts) != 5:
                raise RolloutError(f"{target['host_id']} returned malformed rollback marker")
            receipt = {
                "schema_version": 1,
                "host_id": target["host_id"],
                "revision": manifest["source"]["revision"],
                "artifact_sha256": digest,
                "applied_at": stamp,
                "rollback_dir": marker_parts[1],
                "previous_artifacts": {
                    "binary": marker_parts[2] == "1",
                    "config": marker_parts[3] == "1",
                    "unit": marker_parts[4] == "1",
                },
            }
            receipt_file = temp_root / f"{target['host_id']}.receipt.json"
            receipt_file.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
            scp_to_target(target, receipt_file, f"/tmp/grepmesh-receipt.{stamp}.json")
            result = target_command(target, f"sudo -n install -o root -g root -m 0644 /tmp/grepmesh-receipt.{stamp}.json {q['state_dir']}/receipt.json\n")
            if result.returncode:
                raise RolloutError(f"{target['host_id']} receipt failed: {result.stderr.strip()}")


def rollback(manifest: dict[str, Any], supplied_confirmation: str) -> None:
    expected = rollback_confirmation(manifest)
    if supplied_confirmation != expected:
        raise RolloutError("rollback confirmation does not match this manifest")
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    with tempfile.TemporaryDirectory(prefix="grepmesh-rollback-") as temp:
        temp_root = Path(temp)
        for target in manifest["targets"]:
            q = manifest["service"]
            receipt_path = f"{q['state_dir']}/receipt.json"
            receipt_result = target_command(
                target,
                f"set -eu\nsudo -n cat {shlex.quote(receipt_path)}\n",
            )
            if receipt_result.returncode:
                raise RolloutError(
                    f"{target['host_id']} rollback receipt read failed: "
                    f"{receipt_result.stderr.strip() or receipt_result.stdout.strip()}"
                )
            try:
                receipt = json.loads(receipt_result.stdout)
            except json.JSONDecodeError as exc:
                raise RolloutError(f"{target['host_id']} rollback receipt is invalid JSON") from exc
            rollback_dir = receipt.get("rollback_dir")
            previous = receipt.get("previous_artifacts") or {}
            expected_parent = Path(q["state_dir"])
            rollback_path = Path(rollback_dir) if isinstance(rollback_dir, str) else None
            if (
                rollback_path is None
                or rollback_path.parent != expected_parent
                or not rollback_path.name.startswith("rollback-")
            ):
                raise RolloutError(f"{target['host_id']} rollback directory is not trusted")
            if not all(previous.get(key) is True for key in ("binary", "config", "unit")):
                raise RolloutError(
                    f"{target['host_id']} has no complete previous installation in {rollback_dir}"
                )
            script = f"""set -eu
sudo -n test -s {shlex.quote(str(rollback_path / 'binary'))}
sudo -n test -s {shlex.quote(str(rollback_path / 'config.json'))}
sudo -n test -s {shlex.quote(str(rollback_path / 'unit'))}
sudo -n install -o root -g root -m 0755 {shlex.quote(str(rollback_path / 'binary'))} {shlex.quote(manifest['source']['install_path'])}
sudo -n install -o root -g root -m 0644 {shlex.quote(str(rollback_path / 'config.json'))} {shlex.quote(q['config_path'])}
sudo -n install -o root -g root -m 0644 {shlex.quote(str(rollback_path / 'unit'))} /etc/systemd/system/{shlex.quote(q['unit_name'])}
sudo -n systemctl daemon-reload
sudo -n systemctl restart {shlex.quote(q['unit_name'])}
sudo -n systemctl is-active {shlex.quote(q['unit_name'])}
"""
            result = target_command(target, script)
            if result.returncode:
                raise RolloutError(
                    f"{target['host_id']} rollback failed: "
                    f"{result.stderr.strip() or result.stdout.strip()}"
                )
            rollback_receipt = {
                "schema_version": 1,
                "action": "rollback",
                "host_id": target["host_id"],
                "restored_from": str(rollback_path),
                "rolled_back_at": stamp,
                "prior_receipt": receipt,
            }
            receipt_file = temp_root / f"{target['host_id']}.rollback-receipt.json"
            receipt_file.write_text(json.dumps(rollback_receipt, indent=2) + "\n", encoding="utf-8")
            scp_to_target(target, receipt_file, f"/tmp/grepmesh-rollback-receipt.{stamp}.json")
            result = target_command(
                target,
                f"sudo -n install -o root -g root -m 0644 /tmp/grepmesh-rollback-receipt.{stamp}.json "
                f"{q['state_dir']}/last-rollback.json\n",
            )
            if result.returncode:
                raise RolloutError(f"{target['host_id']} rollback receipt failed: {result.stderr.strip()}")


def verify(manifest: dict[str, Any]) -> None:
    for target in manifest["targets"]:
        q = manifest["service"]
        script = f"""set -eu
sudo -n systemctl is-active {shlex.quote(q['unit_name'])}
sudo -n test -s {shlex.quote(q['config_path'])}
sudo -n test -s {shlex.quote(q['state_dir'])}/receipt.json
"""
        result = target_command(target, script)
        if result.returncode:
            raise RolloutError(f"{target['host_id']} verify failed: {result.stderr.strip() or result.stdout.strip()}")
        print(f"{target['host_id']}: active, config and receipt present")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["preview", "apply", "verify", "rollback"])
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--confirm")
    args = parser.parse_args()
    try:
        manifest = load_manifest(args.manifest)
        if args.command == "preview":
            print(json.dumps(preview(manifest), indent=2))
        elif args.command == "apply":
            apply(manifest, args.confirm or "")
            print("apply complete")
        elif args.command == "rollback":
            rollback(manifest, args.confirm or "")
            print("rollback complete")
        else:
            verify(manifest)
        return 0
    except (OSError, subprocess.CalledProcessError, RolloutError, json.JSONDecodeError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
