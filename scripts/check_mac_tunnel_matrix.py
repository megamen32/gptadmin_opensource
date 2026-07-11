#!/usr/bin/env python3
"""Run a GPTAdmin install/tunnel smoke matrix against a locally reachable Mac.

The script runs on the current host and drives the Mac over SSH. Each matrix
cell provisions an isolated hub + shellmcp install on the Mac, verifies the
public tunnel surface, then removes the temporary install and restores any real
GPTAdmin installation that existed before the run.
"""

from __future__ import annotations

import argparse
import base64
import dataclasses
import hashlib
import io
import json
import os
import random
import re
import secrets
import shlex
import string
import subprocess
import sys
import tarfile
import textwrap
import time
from pathlib import Path
from typing import Any, Iterable, Mapping, Sequence
from urllib.parse import parse_qs, urlencode, urlparse

import httpx

REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_ROOT = Path(__file__).resolve().parent
DEFAULT_REPORT_ROOT = REPO_ROOT / "logs" / "mac_tunnel_matrix"

SNAPSHOT_FILES = (
    "go-hub/cmd/gptadmin-hub",
    "go-shellmcp/cmd/shellmcp-go",
    "gptadmin_security.py",
    "gptadmin_build_info.py",
    "public/admin_dashboard.html",
    "public/openapi.yaml",
)

DEFAULT_BACKENDS = ("cloudflare", "ngrok", "frp")
DEFAULT_SCOPES = ("user", "system")

def load_env_file(path: Path, *, override: bool = False) -> None:
    """Load simple KEY=VALUE lines from a local env file without shell eval."""
    if not path.exists():
        return
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        if not key:
            continue
        if override or key not in os.environ:
            os.environ[key] = value


def require_env(name: str) -> str:
    """Return a required environment variable or fail with a clear message."""
    value = os.environ.get(name, "").strip()
    if not value:
        raise HarnessError(
            f"missing required {name}; create scripts/check_mac_tunnel_matrix.env "
            "from scripts/check_mac_tunnel_matrix.env.example or export it"
        )
    return value


DEFAULT_ENV_FILE = SCRIPT_ROOT / "check_mac_tunnel_matrix.env"
load_env_file(DEFAULT_ENV_FILE)

FRP_SERVER_ADDR_DEFAULT = os.environ.get("MAC_TUNNEL_FRP_SERVER_ADDR", "")
FRP_SERVER_PORT_DEFAULT = os.environ.get("MAC_TUNNEL_FRP_SERVER_PORT", "7000")
FRP_TOKEN_DEFAULT = os.environ.get("MAC_TUNNEL_FRP_TOKEN", "")
FRP_DOMAIN_DEFAULT = os.environ.get("MAC_TUNNEL_FRP_DOMAIN", FRP_SERVER_ADDR_DEFAULT)

REMOTE_ARCHIVE_PY = r"""
from __future__ import annotations

import glob
import json
import os
import shlex
import shutil
import subprocess
from pathlib import Path

uid = str(os.getuid())
include_system = os.environ.get("INCLUDE_SYSTEM") == "1"
archive_root = Path(os.environ["ARCHIVE_ROOT"])
archive_root.mkdir(parents=True, exist_ok=True)
archive_file = archive_root / "gptadmin-install.tar.gz"
state_file = archive_root / "archive_state.json"

patterns = [
    "~/.local/share/gptadmin",
    "~/.config/gptadmin",
    "~/Library/LaunchAgents/com.gptadmin*.plist",
    "~/Library/Logs/gptadmin",
]
if include_system:
    patterns.extend([
        "/opt/gptadmin",
        "/etc/gptadmin",
        "/Library/LaunchDaemons/com.gptadmin*.plist",
        "/var/log/gptadmin",
    ])


def run(cmd: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(cmd, check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


def loaded_labels(system: bool) -> list[str]:
    cmd = ["sudo", "-n", "launchctl", "list"] if system else ["launchctl", "list"]
    cp = run(cmd)
    labels: list[str] = []
    for line in (cp.stdout or "").splitlines():
        parts = line.split()
        if len(parts) >= 3:
            label = parts[-1]
            if label.startswith("com.gptadmin"):
                labels.append(label)
    return sorted(set(labels))


def stop_label(label: str, system: bool) -> list[str]:
    prefix = ["sudo", "-n"] if system else []
    domain = f"system/{label}" if system else f"gui/{uid}/{label}"
    unit = f"/Library/LaunchDaemons/{label}.plist" if system else str(Path.home() / "Library/LaunchAgents" / f"{label}.plist")
    attempts = [
        prefix + ["launchctl", "bootout", domain],
        prefix + ["launchctl", "bootout", "system" if system else f"gui/{uid}", unit],
        prefix + ["launchctl", "remove", label],
        prefix + ["launchctl", "unload", "-w", unit],
    ]
    messages: list[str] = []
    for cmd in attempts:
        cp = run(cmd)
        if cp.returncode != 0 and (cp.stdout or "").strip():
            messages.append("$ " + " ".join(cmd) + "\n" + cp.stdout.strip())
    return messages


def existing_paths() -> list[str]:
    found: list[str] = []
    for pattern in patterns:
        for raw in glob.glob(os.path.expanduser(pattern)):
            path = Path(raw)
            if path.exists():
                found.append(str(path))
    return sorted(set(found))


def remove_path(path: Path) -> None:
    if path.is_symlink() or path.is_file():
        path.unlink(missing_ok=True)
    elif path.is_dir():
        shutil.rmtree(path, ignore_errors=True)


user_labels = loaded_labels(system=False)
system_labels = loaded_labels(system=True) if include_system else []
stop_messages = {}
for label in user_labels:
    stop_messages[label] = stop_label(label, system=False)
for label in system_labels:
    stop_messages[label] = stop_label(label, system=True)

paths = existing_paths()
state = {
    "paths": paths,
    "loaded_user_labels": user_labels,
    "loaded_system_labels": system_labels,
    "stop_messages": stop_messages,
}

if paths:
    if include_system:
        cmd = "printf '%s\\n' \"$MAC_TUNNEL_SUDO_PASSWORD\" | sudo -S -p '' tar -czPf " + shlex.quote(str(archive_file)) + " " + shlex.join(paths)
    else:
        cmd = "tar -czPf " + shlex.quote(str(archive_file)) + " " + shlex.join(paths)
    cp = run(["bash", "-lc", cmd])
    if cp.returncode != 0:
        raise SystemExit(cp.stdout)

for raw in paths:
    path = Path(raw)
    if str(path).startswith(("/opt/", "/etc/", "/Library/", "/var/")):
        cp = run(["bash", "-lc", "printf '%s\\n' \"$MAC_TUNNEL_SUDO_PASSWORD\" | sudo -S -p '' rm -rf " + shlex.quote(str(path))])
        if cp.returncode != 0:
            raise SystemExit(cp.stdout)
    else:
        remove_path(path)

state_file.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print(json.dumps({"archive_file": str(archive_file), "state_file": str(state_file), "path_count": len(paths)}))
"""

REMOTE_RESTORE_PY = r"""
from __future__ import annotations

import json
import os
import shlex
import subprocess
from pathlib import Path

archive_root = Path(os.environ["ARCHIVE_ROOT"])
include_system = os.environ.get("INCLUDE_SYSTEM") == "1"
archive_file = archive_root / "gptadmin-install.tar.gz"
state_file = archive_root / "archive_state.json"
state = json.loads(state_file.read_text(encoding="utf-8")) if state_file.exists() else {}
uid = str(os.getuid())


def run(cmd: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(cmd, check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


if archive_file.exists():
    if include_system:
        cmd = "printf '%s\\n' \"$MAC_TUNNEL_SUDO_PASSWORD\" | sudo -S -p '' tar -xzPf " + shlex.quote(str(archive_file)) + " -C /"
    else:
        cmd = "tar -xzPf " + shlex.quote(str(archive_file)) + " -C /"
    cp = run(["bash", "-lc", cmd])
    if cp.returncode != 0:
        raise SystemExit(cp.stdout)

messages = []
for label in state.get("loaded_user_labels", []):
    unit = str(Path.home() / "Library/LaunchAgents" / f"{label}.plist")
    for cmd in (
        ["launchctl", "load", "-w", unit],
        ["launchctl", "bootstrap", f"gui/{uid}", unit],
    ):
        cp = run(cmd)
        if cp.returncode == 0:
            break
        if (cp.stdout or "").strip():
            messages.append("$ " + " ".join(cmd) + "\n" + cp.stdout.strip())

for label in state.get("loaded_system_labels", []):
    unit = f"/Library/LaunchDaemons/{label}.plist"
    for cmd in (
        ["sudo", "-n", "launchctl", "load", "-w", unit],
        ["sudo", "-n", "launchctl", "bootstrap", "system", unit],
    ):
        cp = run(cmd)
        if cp.returncode == 0:
            break
        if (cp.stdout or "").strip():
            messages.append("$ " + " ".join(cmd) + "\n" + cp.stdout.strip())

print(json.dumps({"restored": True, "messages": messages[-20:]}))
"""


@dataclasses.dataclass(frozen=True)
class SSHConfig:
    """Connection settings for the remote Mac."""

    user: str
    host: str
    port: int


@dataclasses.dataclass(frozen=True)
class MatrixCell:
    """One matrix entry: install scope + tunnel backend."""

    scope: str
    backend: str
    index: int

    @property
    def slug(self) -> str:
        return f"{self.scope}-{self.backend}"


@dataclasses.dataclass
class CommandResult:
    """Completed local or remote command."""

    argv: list[str]
    returncode: int
    stdout: str
    stderr: str


@dataclasses.dataclass
class CellResult:
    """Serializable cell execution result."""

    cell: MatrixCell
    status: str
    public_url: str | None = None
    reason: str | None = None
    verification: dict[str, Any] | None = None
    remote_paths: dict[str, str] | None = None

    def to_json(self) -> dict[str, Any]:
        """Return a JSON-safe cell summary."""
        return {
            "scope": self.cell.scope,
            "backend": self.cell.backend,
            "slug": self.cell.slug,
            "status": self.status,
            "public_url": self.public_url,
            "reason": self.reason,
            "verification": self.verification or {},
            "remote_paths": self.remote_paths or {},
        }


class HarnessError(RuntimeError):
    """Raised when an unrecoverable harness step fails."""


class SkipCell(RuntimeError):
    """Raised when a matrix cell should be marked as skipped."""


class MacTunnelMatrixHarness:
    """Drive the remote Mac tunnel/install smoke matrix."""

    def __init__(
        self,
        ssh_config: SSHConfig,
        report_dir: Path,
        backends: Sequence[str],
        scopes: Sequence[str],
        keep_remote_temp: bool,
    ) -> None:
        self.ssh_config = ssh_config
        self.report_dir = report_dir
        self.backends = tuple(backends)
        self.scopes = tuple(scopes)
        self.keep_remote_temp = keep_remote_temp
        self.run_id = time.strftime("%Y%m%d-%H%M%S") + "-" + secrets.token_hex(4)
        self.remote_root = f"/tmp/gptadmin-mac-matrix-{self.run_id}"
        self.remote_archive_root = f"{self.remote_root}/archive"
        self.snapshot_path = self.report_dir / "source-snapshot.tgz"
        self.top_report_path = self.report_dir / "report.json"
        self.results: list[CellResult] = []
        self.remote_home = ""
        self.sudo_password = os.environ.get("MAC_TUNNEL_SUDO_PASSWORD", "")

    def run(self) -> int:
        """Execute the full archive -> matrix -> restore flow."""
        self.report_dir.mkdir(parents=True, exist_ok=True)
        self._build_snapshot()
        self._ensure_remote_prereqs()
        self._prepare_remote_root()
        self._upload_file(self.snapshot_path, f"{self.remote_root}/source-snapshot.tgz")
        archive_meta = self._archive_real_install()
        self._write_json(self.report_dir / "archive.json", archive_meta)

        exit_code = 0
        try:
            for cell in self._matrix():
                result = self._run_cell(cell)
                self.results.append(result)
                self._write_top_report()
                if result.status == "failed":
                    exit_code = 1
        finally:
            restore_info: dict[str, Any] = {}
            try:
                restore_info = self._restore_real_install()
                self._write_json(self.report_dir / "restore.json", restore_info)
            finally:
                if not self.keep_remote_temp:
                    self._cleanup_remote_root()
            self._write_top_report(extra={"restore": restore_info})
        return exit_code

    def _matrix(self) -> list[MatrixCell]:
        """Expand the scope/backend matrix with stable indices."""
        cells: list[MatrixCell] = []
        index = 0
        for scope in self.scopes:
            for backend in self.backends:
                cells.append(MatrixCell(scope=scope, backend=backend, index=index))
                index += 1
        return cells

    def _write_top_report(self, extra: Mapping[str, Any] | None = None) -> None:
        """Persist the current top-level run summary."""
        counts = {"passed": 0, "failed": 0, "skipped": 0}
        for result in self.results:
            counts[result.status] = counts.get(result.status, 0) + 1
        payload: dict[str, Any] = {
            "run_id": self.run_id,
            "ssh": dataclasses.asdict(self.ssh_config),
            "report_dir": str(self.report_dir),
            "remote_root": self.remote_root,
            "keep_remote_temp": self.keep_remote_temp,
            "counts": counts,
            "results": [result.to_json() for result in self.results],
        }
        if extra:
            payload.update(dict(extra))
        self._write_json(self.top_report_path, payload)

    def _run_cell(self, cell: MatrixCell) -> CellResult:
        """Run one matrix cell end-to-end."""
        cell_dir = self.report_dir / cell.slug
        cell_dir.mkdir(parents=True, exist_ok=True)
        ssh_log = cell_dir / "ssh.log"
        cell_root = f"{self.remote_root}/cells/{cell.slug}"
        remote_paths = self._remote_paths_for_cell(cell, cell_root)
        remote_meta = {"cell_root": cell_root, **remote_paths}
        cleanup_runtime: Mapping[str, Any] = {"scope": cell.scope, **remote_paths}
        self._write_json(cell_dir / "remote_paths.json", remote_meta)
        try:
            runtime = self._prepare_remote_cell(cell, cell_root, ssh_log)
            cleanup_runtime = runtime
            runtime = self._install_or_skip_backend(cell, runtime, ssh_log)
            cleanup_runtime = runtime
            public_url = self._determine_public_url(cell, runtime, ssh_log)
            self._update_public_origin(runtime, public_url, ssh_log)
            self._start_shellmcp(runtime, ssh_log)
            self._approve_shell_if_needed(public_url, runtime, ssh_log)
            verification = self._verify_cell(cell, runtime, public_url)
            self._write_json(cell_dir / "verification.json", verification)
            self._collect_remote_logs(runtime, cell_dir, ssh_log)
            result = CellResult(
                cell=cell,
                status="passed",
                public_url=public_url,
                verification=verification,
                remote_paths=remote_meta,
            )
        except SkipCell as exc:
            self._collect_remote_logs_best_effort(remote_paths, cell_dir, ssh_log)
            result = CellResult(cell=cell, status="skipped", reason=str(exc), remote_paths=remote_meta)
        except Exception as exc:
            self._collect_remote_logs_best_effort(remote_paths, cell_dir, ssh_log)
            result = CellResult(cell=cell, status="failed", reason=str(exc), remote_paths=remote_meta)
        finally:
            try:
                self._cleanup_cell(cleanup_runtime, ssh_log)
            except Exception as cleanup_exc:
                if result.status == "passed":
                    result.status = "failed"
                    result.reason = f"cleanup failed: {cleanup_exc}"
                elif result.reason:
                    result.reason = f"{result.reason}; cleanup failed: {cleanup_exc}"
            self._write_json(cell_dir / "summary.json", result.to_json())
        return result

    def _prepare_remote_cell(self, cell: MatrixCell, cell_root: str, ssh_log: Path) -> dict[str, Any]:
        """Create an isolated remote install layout and start hub + shellmcp."""
        ports = self._ports_for_cell(cell.index)
        service_prefix = f"com.gptadmin.matrix.{self.run_id}.{cell.scope}.{cell.backend}".replace("-", "")
        shell_name = f"matrix-{cell.scope}-{cell.backend}-{self.run_id}"
        install_root = self._install_root(cell, cell_root)
        config_dir = self._config_root(cell, cell_root)
        logs_dir = f"{cell_root}/logs"
        run_dir = f"{cell_root}/run"
        src_dir = f"{cell_root}/src"
        venv_dir = f"{cell_root}/venv"
        bin_dir = f"{cell_root}/bin"
        env_path = f"{cell_root}/env.sh"
        tunnel_label = f"{service_prefix}.tunnel"
        runtime = {
            "cell": cell.slug,
            "scope": cell.scope,
            "backend": cell.backend,
            "cell_root": cell_root,
            "install_root": install_root,
            "config_dir": config_dir,
            "logs_dir": logs_dir,
            "run_dir": run_dir,
            "src_dir": src_dir,
            "venv_dir": venv_dir,
            "bin_dir": bin_dir,
            "env_path": env_path,
            "snapshot_path": f"{self.remote_root}/source-snapshot.tgz",
            "hub_port": ports["hub_port"],
            "shell_port": ports["shell_port"],
            "ngrok_api_port": ports["ngrok_api_port"],
            "hub_label": f"{service_prefix}.hub",
            "shell_label": f"{service_prefix}.shellmcp",
            "tunnel_label": tunnel_label,
            "hub_plist": self._plist_path(cell.scope, f"{service_prefix}.hub"),
            "shell_plist": self._plist_path(cell.scope, f"{service_prefix}.shellmcp"),
            "tunnel_plist": self._plist_path(cell.scope, tunnel_label),
            "hub_log": f"{logs_dir}/hub.log",
            "shell_log": f"{logs_dir}/shellmcp.log",
            "tunnel_log": f"{logs_dir}/tunnel.log",
            "python": f"{venv_dir}/bin/python",
            "pip": f"{venv_dir}/bin/pip",
            "hub_wrapper": f"{run_dir}/run_hub.sh",
            "shell_wrapper": f"{run_dir}/run_shellmcp.sh",
            "tunnel_wrapper": f"{run_dir}/run_tunnel.sh",
            "shell_name": shell_name,
            "ctl_token": secrets.token_hex(16),
            "shellmcp_token": secrets.token_hex(16),
            "admin_password": "pw-" + secrets.token_urlsafe(12),
            "oauth_client_secret": secrets.token_hex(32),
            "frp_server_addr": FRP_SERVER_ADDR_DEFAULT,
            "frp_server_port": FRP_SERVER_PORT_DEFAULT,
            "frp_token": FRP_TOKEN_DEFAULT,
            "frp_domain": FRP_DOMAIN_DEFAULT,
            "frp_subdomain": f"gptadmin-{self.run_id}-{cell.scope[:1]}{cell.backend[:2]}-{random.randint(100, 999)}",
        }
        self._remote_mkdirs([cell_root, logs_dir, run_dir, src_dir, bin_dir], ssh_log)
        if cell.scope == "system":
            self._remote_bash(
                ssh_log,
                f"""
                set -euo pipefail
                sudo -n mkdir -p {shlex.quote(install_root)} {shlex.quote(config_dir)}
                """,
            )
        else:
            self._remote_mkdirs([install_root, config_dir], ssh_log)
        self._remote_bash(
            ssh_log,
            f"""
            set -euo pipefail
            rm -rf {shlex.quote(src_dir)}
            mkdir -p {shlex.quote(src_dir)}
            tar -xzf {shlex.quote(runtime["snapshot_path"])} -C {shlex.quote(src_dir)}
            python3 -m venv {shlex.quote(venv_dir)}
            {shlex.quote(runtime["python"])} -m pip install --upgrade pip
            {shlex.quote(runtime["pip"])} install fastapi 'uvicorn[standard]' httpx pydantic cryptography requests psutil
            """,
        )
        self._write_remote_env(runtime, ssh_log)
        self._write_remote_wrappers_and_plists(runtime, ssh_log)
        self._restart_service(runtime["hub_label"], runtime["hub_plist"], cell.scope, ssh_log)
        self._wait_remote_http_ok(runtime["hub_port"], "/version")
        return runtime

    def _install_or_skip_backend(self, cell: MatrixCell, runtime: Mapping[str, Any], ssh_log: Path) -> dict[str, Any]:
        """Ensure the requested backend is usable, then start its tunnel service."""
        backend = cell.backend
        if backend == "ngrok":
            token = os.environ.get("MAC_TUNNEL_NGROK_AUTHTOKEN") or self._read_remote_env("NGROK_AUTHTOKEN")
            if not token:
                raise SkipCell("ngrok skipped: missing MAC_TUNNEL_NGROK_AUTHTOKEN and remote NGROK_AUTHTOKEN")
            ngrok_bin = self._remote_which("ngrok")
            if not ngrok_bin:
                raise SkipCell("ngrok skipped: binary not installed on Mac")
            runtime = dict(runtime)
            runtime["ngrok_bin"] = ngrok_bin
            runtime["ngrok_authtoken"] = token
            self._write_remote_env(runtime, ssh_log)
            self._write_remote_wrappers_and_plists(runtime, ssh_log)
        elif backend == "cloudflare":
            self._remote_bash(
                ssh_log,
                f"""
                set -euo pipefail
                if ! command -v cloudflared >/dev/null 2>&1; then
                  ARCH="$(uname -m)"
                  if [ "$ARCH" = "arm64" ]; then ASSET=arm64; else ASSET=amd64; fi
                  URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-${{ASSET}}.tgz"
                  TMPDIR="$(mktemp -d)"
                  trap 'rm -rf "$TMPDIR"' EXIT
                  curl -fL "$URL" -o "$TMPDIR/cloudflared.tgz"
                  tar -xzf "$TMPDIR/cloudflared.tgz" -C "$TMPDIR"
                  BIN_SRC="$(find "$TMPDIR" -maxdepth 3 -type f -name cloudflared -print -quit)"
                  [ -n "$BIN_SRC" ]
                  cp "$BIN_SRC" {shlex.quote(str(runtime["bin_dir"]))}/cloudflared
                  chmod +x {shlex.quote(str(runtime["bin_dir"]))}/cloudflared
                fi
                """,
            )
        elif backend == "frp":
            if not runtime.get("frp_server_addr"):
                runtime = dict(runtime)
                runtime["frp_server_addr"] = require_env("MAC_TUNNEL_FRP_SERVER_ADDR")
            if not runtime.get("frp_token"):
                runtime = dict(runtime)
                runtime["frp_token"] = require_env("MAC_TUNNEL_FRP_TOKEN")
            if not runtime.get("frp_domain"):
                runtime = dict(runtime)
                runtime["frp_domain"] = runtime["frp_server_addr"]
            self._write_remote_env(runtime, ssh_log)
            self._write_remote_wrappers_and_plists(runtime, ssh_log)
            self._remote_bash(
                ssh_log,
                f"""
                set -euo pipefail
                if ! command -v frpc >/dev/null 2>&1 && [ ! -x {shlex.quote(str(runtime["bin_dir"]))}/frpc ]; then
                  ARCH="$(uname -m)"
                  case "$ARCH" in
                    arm64|aarch64) FRP_ARCH=arm64 ;;
                    *) FRP_ARCH=amd64 ;;
                  esac
                  VER="0.64.0"
                  URL="https://github.com/fatedier/frp/releases/download/v${{VER}}/frp_${{VER}}_darwin_${{FRP_ARCH}}.tar.gz"
                  TMPDIR="$(mktemp -d)"
                  trap 'rm -rf "$TMPDIR"' EXIT
                  curl -fL "$URL" -o "$TMPDIR/frp.tgz"
                  tar -xzf "$TMPDIR/frp.tgz" -C "$TMPDIR"
                  cp "$TMPDIR/frp_${{VER}}_darwin_${{FRP_ARCH}}/frpc" {shlex.quote(str(runtime["bin_dir"]))}/frpc
                  chmod +x {shlex.quote(str(runtime["bin_dir"]))}/frpc
                fi
                """,
            )
        self._restart_service(str(runtime["tunnel_label"]), str(runtime["tunnel_plist"]), cell.scope, ssh_log)
        return dict(runtime)

    def _start_shellmcp(self, runtime: Mapping[str, Any], ssh_log: Path) -> None:
        """Start shellmcp after the hub public origin and tunnel are ready."""
        self._restart_service(str(runtime["shell_label"]), str(runtime["shell_plist"]), str(runtime["scope"]), ssh_log)

    def _determine_public_url(self, cell: MatrixCell, runtime: Mapping[str, Any], ssh_log: Path) -> str:
        """Resolve the public URL produced by the selected tunnel."""
        if cell.backend == "frp":
            subdomain = str(runtime["frp_subdomain"])
            domain = str(runtime["frp_domain"])
            return f"https://{subdomain}.{domain}".rstrip("/")
        log_path = str(runtime["tunnel_log"])
        if cell.backend == "cloudflare":
            pattern = r"https://[A-Za-z0-9.-]+\.trycloudflare\.com"
        else:
            pattern = r"https://[A-Za-z0-9.-]+\.ngrok(?:-free)?\.app"
        deadline = time.time() + 120
        while time.time() < deadline:
            result = self._remote_bash(
                ssh_log,
                f"""
                set -euo pipefail
                if [ -f {shlex.quote(log_path)} ]; then
                  python3 - <<'PY'
import pathlib, re
path = pathlib.Path({log_path!r})
text = path.read_text(encoding='utf-8', errors='ignore')
m = re.search({pattern!r}, text)
print(m.group(0) if m else "")
PY
                fi
                """,
            )
            url = result.stdout.strip().splitlines()[-1] if result.stdout.strip() else ""
            if url.startswith("https://"):
                return url.rstrip("/")
            time.sleep(2)
        raise HarnessError(f"tunnel URL not found for {cell.slug}")

    def _update_public_origin(self, runtime: Mapping[str, Any], public_url: str, ssh_log: Path) -> None:
        """Rewrite env with the final public origin and restart the hub."""
        updated = dict(runtime)
        updated["public_origin"] = public_url
        updated["mcp_resource"] = public_url
        self._write_remote_env(updated, ssh_log)
        self._restart_service(str(runtime["hub_label"]), str(runtime["hub_plist"]), str(runtime["scope"]), ssh_log)
        self._wait_remote_http_ok(int(runtime["hub_port"]), "/version")

    def _approve_shell_if_needed(self, public_url: str, runtime: Mapping[str, Any], ssh_log: Path | None = None) -> None:
        """Approve the fresh shellmcp instance when the hub marks it as pending.

        Approval uses the Mac-local hub URL to avoid tunnel/DNS flakiness while the
        public URL is still verified separately by _verify_cell().
        """
        approve_body = json.dumps({
            "target": "hub",
            "tool_name": "approve_pending_server",
            "arguments": {"name": str(runtime["shell_name"])},
            "timeout": 30,
        })
        script = f"""
        set +e
        deadline=$((SECONDS + 90))
        base='http://127.0.0.1:{runtime['hub_port']}'
        token={shlex.quote(str(runtime['ctl_token']))}
        shell_name={shlex.quote(str(runtime['shell_name']))}
        while [ "$SECONDS" -lt "$deadline" ]; do
          json="$(curl -fsS --max-time 10 -H "Authorization: Bearer $token" "$base/servers" 2>/dev/null)"
          rc=$?
          if [ "$rc" -eq 0 ]; then
            JSON_PAYLOAD="$json" python3 - "$shell_name" <<'PY'
import json, os, sys
name=sys.argv[1]
data=json.loads(os.environ.get('JSON_PAYLOAD') or '{{}}')
for item in data.get('servers') or []:
    if item.get('name') == name and (item.get('alive') or item.get('status') == 'active'):
        raise SystemExit(0)
for item in data.get('pending') or []:
    if item.get('name') == name:
        raise SystemExit(2)
raise SystemExit(1)
PY
            pyrc=$?
            if [ "$pyrc" -eq 0 ]; then exit 0; fi
            if [ "$pyrc" -eq 2 ]; then
              curl -fsS --max-time 20 -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
                -d {shlex.quote(approve_body)} \
                "$base/mcp-relay/call" >/dev/null || true
            fi
          fi
          sleep 2
        done
        exit 1
        """
        try:
            self._remote_bash(ssh_log or (self.report_dir / "bootstrap-ssh.log"), script)
        except HarnessError as exc:
            raise HarnessError(f"shellmcp did not become active for {runtime['cell']}: {exc}") from exc

    def _verify_cell(self, cell: MatrixCell, runtime: Mapping[str, Any], public_url: str) -> dict[str, Any]:
        """Run the full verification suite for one cell."""
        verification: dict[str, Any] = {"checks": []}
        ctl_headers = {"Authorization": f"Bearer {runtime['ctl_token']}"}
        shell_name = str(runtime["shell_name"])

        local_version = self._remote_http_json(int(runtime["hub_port"]), "/version")
        verification["checks"].append({"name": "local_version", "ok": bool(local_version.get("hub_id")) or bool(local_version.get("component")), "response": local_version})

        with httpx.Client(timeout=30.0, follow_redirects=False) as client:
            public_version = client.get(f"{public_url}/version")
            public_version.raise_for_status()
            public_version_json = public_version.json()
            verification["checks"].append({"name": "public_version", "ok": True, "response": public_version_json})

            servers_resp = client.get(f"{public_url}/servers", headers=ctl_headers)
            servers_resp.raise_for_status()
            servers_json = servers_resp.json()
            verification["checks"].append({"name": "servers", "ok": True, "response": servers_json})

            server_records = [item for item in (servers_json.get("servers") or []) if item.get("name") == shell_name]
            if not server_records:
                raise HarnessError(f"server {shell_name} not listed after approval")
            verification["checks"].append({"name": "server_record", "ok": bool(server_records[0].get("alive") or server_records[0].get("status") == "active"), "response": server_records[0]})

            exec_resp = client.post(
                f"{public_url}/srv/exec",
                params={"server": shell_name},
                headers=ctl_headers,
                json={"cmd": "printf 'matrix_exec_ok\\n'", "timeout": 20},
            )
            exec_resp.raise_for_status()
            exec_json = exec_resp.json()
            verification["checks"].append({"name": "srv_exec", "ok": "matrix_exec_ok" in json.dumps(exec_json), "response": exec_json})

            admin_html = client.get(f"{public_url}/admin", headers=ctl_headers)
            admin_html.raise_for_status()
            verification["checks"].append({"name": "admin_html", "ok": "admin_dashboard" in admin_html.text or "MCP tools tester" in admin_html.text, "response": {"status_code": admin_html.status_code}})

            overview = client.get(f"{public_url}/admin/api/overview", headers=ctl_headers)
            overview.raise_for_status()
            overview_json = overview.json()
            verification["checks"].append({"name": "admin_overview", "ok": True, "response": overview_json})

            protected_resource = client.get(f"{public_url}/.well-known/oauth-protected-resource")
            protected_resource.raise_for_status()
            protected_json = protected_resource.json()
            auth_server = client.get(f"{public_url}/.well-known/oauth-authorization-server")
            auth_server.raise_for_status()
            auth_server_json = auth_server.json()
            verification["checks"].append({"name": "oauth_metadata", "ok": True, "response": {"resource": protected_json, "authorization_server": auth_server_json}})

            oauth = self._complete_oauth_flow(client, public_url, runtime)
            verification["oauth"] = oauth
            oauth_headers = {"Authorization": f"Bearer {oauth['access_token']}"}

            mcp_get = client.get(f"{public_url}/mcp", headers=oauth_headers)
            mcp_get.raise_for_status()
            mcp_get_json = mcp_get.json()
            verification["checks"].append({"name": "mcp_get", "ok": True, "response": mcp_get_json})

            initialize_json = self._mcp_rpc(client, public_url, oauth_headers, req_id=1, method="initialize", params={})
            verification["checks"].append({"name": "mcp_initialize", "ok": "result" in initialize_json, "response": initialize_json})

            tools_list_json = self._mcp_rpc(client, public_url, oauth_headers, req_id=2, method="tools/list", params={})
            verification["checks"].append({"name": "mcp_tools_list", "ok": "result" in tools_list_json, "response": tools_list_json})

            call_json = self._mcp_rpc(
                client,
                public_url,
                oauth_headers,
                req_id=3,
                method="tools/call",
                params={"name": "list_mcp_agents", "arguments": {}},
            )
            agents = self._extract_tool_agents(call_json)
            if not any(agent.get("agent_id") == f"shell:{shell_name}" or agent.get("name") == shell_name for agent in agents):
                raise HarnessError(f"installed shell agent not visible in MCP agent list for {cell.slug}")
            verification["checks"].append({"name": "mcp_tools_call_list_mcp_agents", "ok": True, "response": call_json})
        return verification

    def _complete_oauth_flow(self, client: httpx.Client, public_url: str, runtime: Mapping[str, Any]) -> dict[str, Any]:
        """Execute a full PKCE authorization-code flow against the public hub."""
        verifier = self._pkce_verifier()
        challenge = self._pkce_challenge(verifier)
        redirect_uri = "http://127.0.0.1/oauth/callback"
        state = secrets.token_urlsafe(10)
        register_resp = client.post(
            f"{public_url}/register",
            json={
                "client_name": "mac-tunnel-matrix",
                "redirect_uris": [redirect_uri],
                "grant_types": ["authorization_code"],
                "response_types": ["code"],
                "scope": "gptadmin.read gptadmin.exec",
            },
        )
        register_resp.raise_for_status()
        authorize_params = {
            "redirect_uri": redirect_uri,
            "state": state,
            "code_challenge": challenge,
            "client_id": "chatgpt-dynamic",
            "resource": public_url,
            "scope": "gptadmin.read gptadmin.exec",
        }
        authorize_get = client.get(f"{public_url}/authorize", params=authorize_params)
        authorize_get.raise_for_status()
        if "GPTAdmin MCP Authorization" not in authorize_get.text:
            raise HarnessError("authorize page did not render expected HTML")
        authorize_post = client.post(
            f"{public_url}/authorize",
            content=urlencode({**authorize_params, "password": runtime["admin_password"]}),
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        if authorize_post.status_code != 302:
            raise HarnessError(f"authorize POST returned {authorize_post.status_code}: {authorize_post.text}")
        location = authorize_post.headers.get("Location", "")
        code = parse_qs(urlparse(location).query).get("code", [""])[0]
        if not code:
            raise HarnessError("oauth redirect did not include code")
        token_resp = client.post(
            f"{public_url}/token",
            content=urlencode(
                {
                    "grant_type": "authorization_code",
                    "code": code,
                    "redirect_uri": redirect_uri,
                    "client_id": "chatgpt-dynamic",
                    "code_verifier": verifier,
                    "resource": public_url,
                }
            ),
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        token_resp.raise_for_status()
        token_json = token_resp.json()
        if not token_json.get("access_token"):
            raise HarnessError("token endpoint did not return access_token")
        return {
            "register": register_resp.json(),
            "authorize_location": location,
            "token": token_json,
            "access_token": token_json["access_token"],
        }

    def _mcp_rpc(
        self,
        client: httpx.Client,
        public_url: str,
        headers: Mapping[str, str],
        req_id: int,
        method: str,
        params: Mapping[str, Any],
    ) -> dict[str, Any]:
        """Send one JSON-RPC MCP request and return the decoded response."""
        response = client.post(
            f"{public_url}/mcp",
            headers=headers,
            json={"jsonrpc": "2.0", "id": req_id, "method": method, "params": dict(params)},
        )
        response.raise_for_status()
        payload = response.json()
        if "error" in payload:
            raise HarnessError(f"MCP {method} returned error: {payload['error']}")
        return payload

    def _collect_remote_logs(self, runtime: Mapping[str, Any], cell_dir: Path, ssh_log: Path) -> None:
        """Download hub/shell/tunnel logs after a cell finishes."""
        remote_logs_dir = str(runtime["logs_dir"])
        local_logs_dir = cell_dir / "remote_logs"
        local_logs_dir.mkdir(parents=True, exist_ok=True)
        self._download_remote_dir(remote_logs_dir, local_logs_dir, ssh_log)
        for name in ("hub.log", "shellmcp.log", "tunnel.log"):
            path = local_logs_dir / name
            if not path.exists():
                path.write_text("", encoding="utf-8")

    def _collect_remote_logs_best_effort(self, remote_paths: Mapping[str, str], cell_dir: Path, ssh_log: Path) -> None:
        """Try to download logs even if the cell failed partway through."""
        try:
            self._collect_remote_logs(remote_paths, cell_dir, ssh_log)
        except Exception:
            return

    def _cleanup_cell(self, runtime: Mapping[str, Any], ssh_log: Path) -> None:
        """Stop services and delete the temporary files for one cell."""
        scope = str(runtime.get("scope"))
        for key in ("tunnel_label", "shell_label", "hub_label"):
            label = str(runtime.get(key) or "")
            plist = str(runtime.get(key.replace("_label", "_plist")) or "")
            if label and plist:
                self._stop_service(label, plist, scope, ssh_log)
        if not self.keep_remote_temp:
            rm_prefix = "sudo -n " if scope == "system" else ""
            self._remote_bash(
                ssh_log,
                f"""
                set -euo pipefail
                {rm_prefix}rm -rf {shlex.quote(str(runtime["cell_root"]))}
                {rm_prefix}rm -f {shlex.quote(str(runtime["hub_plist"]))} {shlex.quote(str(runtime["shell_plist"]))} {shlex.quote(str(runtime["tunnel_plist"]))}
                """,
            )

    def _archive_real_install(self) -> dict[str, Any]:
        """Archive and remove any existing real GPTAdmin install on the Mac."""
        result = self._remote_python(REMOTE_ARCHIVE_PY, {"ARCHIVE_ROOT": self.remote_archive_root, "INCLUDE_SYSTEM": "1" if "system" in self.scopes else "0"})
        return json.loads(result.stdout.strip().splitlines()[-1])

    def _restore_real_install(self) -> dict[str, Any]:
        """Restore the archived GPTAdmin install after the matrix completes."""
        result = self._remote_python(REMOTE_RESTORE_PY, {"ARCHIVE_ROOT": self.remote_archive_root, "INCLUDE_SYSTEM": "1" if "system" in self.scopes else "0"})
        return json.loads(result.stdout.strip().splitlines()[-1])

    def _build_snapshot(self) -> None:
        """Pack the minimum repo subset needed for source-mode remote installs."""
        with tarfile.open(self.snapshot_path, "w:gz") as tar:
            for rel in SNAPSHOT_FILES:
                path = REPO_ROOT / rel
                if not path.exists():
                    raise HarnessError(f"snapshot file missing: {path}")
                tar.add(path, arcname=rel)

    def _ensure_remote_prereqs(self) -> None:
        """Fail fast when the Mac cannot satisfy required basics."""
        result = self._remote_bash(
            self.report_dir / "bootstrap-ssh.log",
            f"""
            set -euo pipefail
            python3 --version
            curl --version >/dev/null
            tar --version >/dev/null
            if [ {shlex.quote('1' if 'system' in self.scopes else '0')} = 1 ]; then
              sudo -n true
            fi
            python3 - <<'PY'
from pathlib import Path
print(Path.home())
PY
            """,
        )
        lines = [line.strip() for line in result.stdout.splitlines() if line.strip()]
        if not lines:
            raise HarnessError("failed to resolve remote home directory")
        self.remote_home = lines[-1]

    def _prepare_remote_root(self) -> None:
        """Create the top-level remote workspace."""
        self._remote_bash(
            self.report_dir / "bootstrap-ssh.log",
            f"""
            set -euo pipefail
            mkdir -p {shlex.quote(self.remote_root)} {shlex.quote(self.remote_root)}/cells {shlex.quote(self.remote_archive_root)}
            """,
        )

    def _cleanup_remote_root(self) -> None:
        """Delete the remote workspace after restore."""
        self._remote_bash(
            self.report_dir / "bootstrap-ssh.log",
            f"""
            set +e
            rm -rf {shlex.quote(self.remote_root)}
            rc=$?
            if [ "$rc" -ne 0 ] && command -v sudo >/dev/null 2>&1; then
              sudo -n rm -rf {shlex.quote(self.remote_root)}
              rc=$?
            fi
            exit "$rc"
            """,
        )

    def _remote_paths_for_cell(self, cell: MatrixCell, cell_root: str) -> dict[str, str]:
        """Return the key remote file locations for log collection and cleanup."""
        scope_dir = self._plist_base_dir(cell.scope)
        service_prefix = f"com.gptadmin.matrix.{self.run_id}.{cell.scope}.{cell.backend}".replace("-", "")
        return {
            "cell_root": cell_root,
            "logs_dir": f"{cell_root}/logs",
            "hub_log": f"{cell_root}/logs/hub.log",
            "shell_log": f"{cell_root}/logs/shellmcp.log",
            "tunnel_log": f"{cell_root}/logs/tunnel.log",
            "hub_plist": f"{scope_dir}/{service_prefix}.hub.plist",
            "shell_plist": f"{scope_dir}/{service_prefix}.shellmcp.plist",
            "tunnel_plist": f"{scope_dir}/{service_prefix}.tunnel.plist",
        }

    def _write_remote_env(self, runtime: Mapping[str, Any], ssh_log: Path) -> None:
        """Render the env.sh file used by all launchd wrappers."""
        env = {
            "PYTHONUNBUFFERED": "1",
            "GPTADMIN_CONFIG_DIR": str(runtime["config_dir"]),
            "GPTADMIN_AUDIT_LOG": f"{runtime['logs_dir']}/hub-audit.log",
            "SHELLMCP_AUDIT_LOG": f"{runtime['logs_dir']}/shellmcp-audit.log",
            "LOG_LEVEL": "INFO",
            "CTL_TOKEN": str(runtime["ctl_token"]),
            "ADMIN_PASSWORD": str(runtime["admin_password"]),
            "OAUTH_CLIENT_SECRET": str(runtime["oauth_client_secret"]),
            "PUBLIC_ORIGIN": str(runtime.get("public_origin") or ""),
            "MCP_RESOURCE": str(runtime.get("mcp_resource") or ""),
            "HUB_BIND": "127.0.0.1",
            "HUB_PORT": str(runtime["hub_port"]),
            "SHELLMCP_TOKEN": str(runtime["shellmcp_token"]),
            "SHELLMCP_NAME": str(runtime["shell_name"]),
            "SHELLMCP_IDENTITY_DIR": f"{runtime['config_dir']}/shellmcp-identity",
            "SHELLMCP_DEFAULT_HOME": self.remote_home,
            "SHELLMCP_DEFAULT_CWD": str(runtime["src_dir"]),
            "SHELLMCP_DEFAULT_USER": self.ssh_config.user,
            "HUB_URL": f"http://127.0.0.1:{runtime['hub_port']}",
            "QUEUE_URL": f"http://127.0.0.1:{runtime['hub_port']}/queue",
            "QUEUE_TRANSPORT": "long_poll",
            "QUEUE_LONG_POLL_TIMEOUT_S": "20",
        }
        if runtime["backend"] == "ngrok":
            env["NGROK_AUTHTOKEN"] = str(runtime.get("ngrok_authtoken") or "")
            env["NGROK_API_PORT"] = str(runtime["ngrok_api_port"])
        if runtime["backend"] == "frp":
            env["FRP_SERVER_ADDR"] = str(runtime["frp_server_addr"])
            env["FRP_SERVER_PORT"] = str(runtime["frp_server_port"])
            env["FRP_TOKEN"] = str(runtime["frp_token"])
            env["FRP_DOMAIN"] = str(runtime["frp_domain"])
            env["FRP_SUBDOMAIN"] = str(runtime["frp_subdomain"])
        content = "".join(f"export {key}={shlex.quote(value)}\n" for key, value in env.items())
        self._upload_text(content, str(runtime["env_path"]), ssh_log)

    def _write_remote_wrappers_and_plists(self, runtime: Mapping[str, Any], ssh_log: Path) -> None:
        """Create wrapper scripts and launchd plists for hub, shellmcp, and tunnel."""
        hub_wrapper = textwrap.dedent(
            f"""\
            #!/bin/sh
            set -a
            . {shlex.quote(str(runtime["env_path"]))}
            set +a
            cd {shlex.quote(str(runtime["src_dir"]))}
            exec go run ./go-hub/cmd/gptadmin-hub
            """
        )
        shell_wrapper = textwrap.dedent(
            f"""\
            #!/bin/sh
            set -a
            . {shlex.quote(str(runtime["env_path"]))}
            set +a
            cd {shlex.quote(str(runtime["src_dir"]))}
            exec go run ./go-shellmcp/cmd/shellmcp-go
            """
        )
        tunnel_wrapper = self._render_tunnel_wrapper(runtime)
        self._upload_text(hub_wrapper, str(runtime["hub_wrapper"]), ssh_log, chmod="+x")
        self._upload_text(shell_wrapper, str(runtime["shell_wrapper"]), ssh_log, chmod="+x")
        self._upload_text(tunnel_wrapper, str(runtime["tunnel_wrapper"]), ssh_log, chmod="+x")
        self._upload_text(
            self._render_plist(str(runtime["hub_label"]), str(runtime["hub_wrapper"]), str(runtime["hub_log"])),
            str(runtime["hub_plist"]),
            ssh_log,
            sudo=str(runtime["scope"]) == "system",
        )
        self._upload_text(
            self._render_plist(str(runtime["shell_label"]), str(runtime["shell_wrapper"]), str(runtime["shell_log"])),
            str(runtime["shell_plist"]),
            ssh_log,
            sudo=str(runtime["scope"]) == "system",
        )
        self._upload_text(
            self._render_plist(str(runtime["tunnel_label"]), str(runtime["tunnel_wrapper"]), str(runtime["tunnel_log"])),
            str(runtime["tunnel_plist"]),
            ssh_log,
            sudo=str(runtime["scope"]) == "system",
        )

    def _render_tunnel_wrapper(self, runtime: Mapping[str, Any]) -> str:
        """Return the backend-specific tunnel runner."""
        backend = str(runtime["backend"])
        common = textwrap.dedent(
            f"""\
            #!/bin/sh
            set -a
            . {shlex.quote(str(runtime["env_path"]))}
            set +a
            """
        )
        if backend == "cloudflare":
            return common + textwrap.dedent(
                f"""\
                BIN="$(command -v cloudflared || true)"
                if [ -z "$BIN" ]; then BIN={shlex.quote(str(runtime["bin_dir"]))}/cloudflared; fi
                exec "$BIN" tunnel --protocol http2 --url http://127.0.0.1:{runtime["hub_port"]} --no-autoupdate
                """
            )
        if backend == "ngrok":
            return common + textwrap.dedent(
                f"""\
                exec {shlex.quote(str(runtime.get("ngrok_bin") or "ngrok"))} http 127.0.0.1:{runtime["hub_port"]} --authtoken "$NGROK_AUTHTOKEN" --log=stdout --api-addr=127.0.0.1:{runtime["ngrok_api_port"]}
                """
            )
        if backend == "frp":
            frpc_conf = f"{runtime['config_dir']}/frpc.toml"
            return common + textwrap.dedent(
                f"""\
                cat > {shlex.quote(frpc_conf)} <<EOF
                serverAddr = "$FRP_SERVER_ADDR"
                serverPort = $FRP_SERVER_PORT

                [auth]
                token = "$FRP_TOKEN"

                [transport.tls]
                enable = true
                serverName = "$FRP_DOMAIN"

                [[proxies]]
                name = "gptadmin-web-$FRP_SUBDOMAIN"
                type = "http"
                localPort = {runtime["hub_port"]}
                subdomain = "$FRP_SUBDOMAIN"
                EOF
                BIN="$(command -v frpc || true)"
                if [ -z "$BIN" ]; then BIN={shlex.quote(str(runtime["bin_dir"]))}/frpc; fi
                exec "$BIN" -c {shlex.quote(frpc_conf)}
                """
            )
        raise HarnessError(f"unknown backend {backend}")

    def _render_plist(self, label: str, wrapper_path: str, log_path: str) -> str:
        """Render a minimal restart-always launchd plist."""
        return textwrap.dedent(
            f"""\
            <?xml version="1.0" encoding="UTF-8"?>
            <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
            <plist version="1.0">
            <dict>
              <key>Label</key><string>{label}</string>
              <key>ProgramArguments</key>
              <array>
                <string>/bin/sh</string>
                <string>{wrapper_path}</string>
              </array>
              <key>RunAtLoad</key><true/>
              <key>KeepAlive</key><true/>
              <key>StandardOutPath</key><string>{log_path}</string>
              <key>StandardErrorPath</key><string>{log_path}</string>
            </dict>
            </plist>
            """
        )

    def _remote_http_json(self, port: int, path: str) -> dict[str, Any]:
        """Run a curl probe on the Mac and return decoded JSON."""
        result = self._remote_bash(
            self.report_dir / "bootstrap-ssh.log",
            f"""
            set -euo pipefail
            curl -fsS http://127.0.0.1:{port}{path}
            """,
        )
        return json.loads(result.stdout)

    def _wait_remote_http_ok(self, port: int, path: str, timeout_s: int = 60) -> None:
        """Wait until a remote localhost HTTP endpoint responds successfully."""
        deadline = time.time() + timeout_s
        while time.time() < deadline:
            result = self._remote_bash(
                self.report_dir / "bootstrap-ssh.log",
                f"""
                set +e
                curl -fsS --max-time 5 http://127.0.0.1:{port}{path} >/dev/null
                rc=$?
                echo "$rc"
                exit 0
                """,
            )
            rc = result.stdout.strip().splitlines()[-1] if result.stdout.strip() else "1"
            if rc == "0":
                return
            time.sleep(2)
        raise HarnessError(f"remote endpoint http://127.0.0.1:{port}{path} did not become healthy")

    def _pkce_verifier(self) -> str:
        """Generate a compact PKCE verifier."""
        alphabet = string.ascii_letters + string.digits + "-._~"
        return "".join(secrets.choice(alphabet) for _ in range(64))

    def _pkce_challenge(self, verifier: str) -> str:
        """Return the base64url SHA-256 PKCE challenge."""
        digest = hashlib.sha256(verifier.encode("utf-8")).digest()
        return base64.urlsafe_b64encode(digest).decode("ascii").rstrip("=")

    def _extract_tool_agents(self, rpc_payload: Mapping[str, Any]) -> list[dict[str, Any]]:
        """Extract `agents` from an MCP tools/call result."""
        result = rpc_payload.get("result") if isinstance(rpc_payload, Mapping) else None
        if not isinstance(result, Mapping):
            return []
        structured = result.get("structuredContent")
        if isinstance(structured, Mapping) and isinstance(structured.get("agents"), list):
            return [item for item in structured["agents"] if isinstance(item, dict)]
        content = result.get("content")
        if isinstance(content, list):
            for item in content:
                if not isinstance(item, Mapping):
                    continue
                text_value = item.get("text")
                if not isinstance(text_value, str):
                    continue
                try:
                    parsed = json.loads(text_value)
                except json.JSONDecodeError:
                    continue
                if isinstance(parsed, Mapping) and isinstance(parsed.get("agents"), list):
                    return [agent for agent in parsed["agents"] if isinstance(agent, dict)]
        return []

    def _read_remote_env(self, name: str) -> str | None:
        """Read one remote environment variable value if present."""
        result = self._remote_bash(
            self.report_dir / "bootstrap-ssh.log",
            f"""
            set +e
            python3 - <<'PY'
import os
print(os.environ.get({name!r}, ""))
PY
            """,
        )
        value = result.stdout.strip()
        return value or None

    def _remote_which(self, binary: str) -> str | None:
        """Return the remote absolute path to a binary if installed."""
        result = self._remote_bash(
            self.report_dir / "bootstrap-ssh.log",
            f"""
            set +e
            command -v {shlex.quote(binary)} || true
            """,
        )
        value = result.stdout.strip()
        return value or None

    def _install_root(self, cell: MatrixCell, cell_root: str) -> str:
        """Choose the install root used by the matrix cell."""
        if cell.scope == "system":
            return f"/opt/gptadmin-matrix/{self.run_id}/{cell.slug}"
        return f"{cell_root}/install"

    def _config_root(self, cell: MatrixCell, cell_root: str) -> str:
        """Choose the config root used by the matrix cell."""
        if cell.scope == "system":
            return f"/etc/gptadmin-matrix/{self.run_id}/{cell.slug}"
        return f"{cell_root}/config"

    def _plist_base_dir(self, scope: str) -> str:
        """Return the launchd plist directory for a given install scope."""
        if scope == "system":
            return "/Library/LaunchDaemons"
        return f"{self.remote_home}/Library/LaunchAgents"

    def _plist_path(self, scope: str, label: str) -> str:
        """Return the remote plist path for a given scope/label."""
        if scope == "system":
            return f"/Library/LaunchDaemons/{label}.plist"
        return f"{self.remote_home}/Library/LaunchAgents/{label}.plist"

    def _ports_for_cell(self, index: int) -> dict[str, int]:
        """Allocate deterministic non-overlapping ports per matrix cell."""
        base = 39000 + (index * 20)
        return {"hub_port": base + 1, "shell_port": base + 2, "ngrok_api_port": base + 3}

    def _remote_mkdirs(self, paths: Iterable[str], ssh_log: Path) -> None:
        """Create remote directories in one call."""
        quoted = " ".join(shlex.quote(path) for path in paths)
        self._remote_bash(ssh_log, f"set -euo pipefail\nmkdir -p {quoted}\n")

    def _restart_service(self, label: str, plist_path: str, scope: str, ssh_log: Path) -> None:
        """Unload then load a launchd service."""
        self._stop_service(label, plist_path, scope, ssh_log)
        if scope == "system":
            script = f"""
            set -euo pipefail
            sudo -n launchctl load -w {shlex.quote(plist_path)}
            """
        else:
            script = f"""
            set -euo pipefail
            launchctl load -w {shlex.quote(plist_path)}
            """
        self._remote_bash(ssh_log, script)

    def _stop_service(self, label: str, plist_path: str, scope: str, ssh_log: Path) -> None:
        """Best-effort stop/remove a launchd service."""
        if scope == "system":
            script = f"""
            set +e
            sudo -n launchctl bootout system/{shlex.quote(label)} >/dev/null 2>&1 || true
            sudo -n launchctl bootout system {shlex.quote(plist_path)} >/dev/null 2>&1 || true
            sudo -n launchctl remove {shlex.quote(label)} >/dev/null 2>&1 || true
            sudo -n launchctl unload -w {shlex.quote(plist_path)} >/dev/null 2>&1 || true
            exit 0
            """
        else:
            script = f"""
            set +e
            launchctl bootout gui/$(id -u)/{shlex.quote(label)} >/dev/null 2>&1 || true
            launchctl bootout gui/$(id -u) {shlex.quote(plist_path)} >/dev/null 2>&1 || true
            launchctl remove {shlex.quote(label)} >/dev/null 2>&1 || true
            launchctl unload -w {shlex.quote(plist_path)} >/dev/null 2>&1 || true
            exit 0
            """
        self._remote_bash(ssh_log, script)

    def _upload_text(self, content: str, remote_path: str, ssh_log: Path, chmod: str | None = None, sudo: bool = False) -> None:
        """Upload a small text file through SSH without a temporary local file."""
        encoded = base64.b64encode(content.encode("utf-8")).decode("ascii")
        dest = shlex.quote(remote_path)
        script = f"""
        set -euo pipefail
        mkdir -p $(dirname {dest})
        python3 - <<'PY'
import base64
from pathlib import Path
content = base64.b64decode({encoded!r})
path = Path({remote_path!r})
path.parent.mkdir(parents=True, exist_ok=True)
path.write_bytes(content)
PY
        """
        if chmod:
            script += f"\nchmod {chmod} {dest}\n"
        if sudo:
            temp_path = f"{self.remote_root}/tmp-upload-{secrets.token_hex(4)}"
            self._upload_text(content, temp_path, ssh_log, chmod=chmod, sudo=False)
            self._remote_bash(
                ssh_log,
                f"""
                set -euo pipefail
                sudo -n mkdir -p $(dirname {dest})
                sudo -n cp {shlex.quote(temp_path)} {dest}
                sudo -n rm -f {shlex.quote(temp_path)}
                """,
            )
            return
        self._remote_bash(ssh_log, script)

    def _upload_file(self, local_path: Path, remote_path: str) -> None:
        """Copy a local file to the remote Mac with scp."""
        argv = [
            "scp",
            "-P",
            str(self.ssh_config.port),
            str(local_path),
            f"{self.ssh_config.user}@{self.ssh_config.host}:{remote_path}",
        ]
        result = subprocess.run(argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if result.returncode != 0:
            raise HarnessError(f"scp upload failed: {result.stderr.strip() or result.stdout.strip()}")

    def _download_remote_dir(self, remote_dir: str, local_dir: Path, ssh_log: Path) -> None:
        """Download a remote directory tree to the local report folder.

        Use a tar stream instead of `scp remote/.`: OpenSSH's SFTP-backed scp can
        reject that source with "unexpected filename: ." on macOS.
        """
        local_dir.mkdir(parents=True, exist_ok=True)
        argv = [
            "ssh",
            "-p",
            str(self.ssh_config.port),
            f"{self.ssh_config.user}@{self.ssh_config.host}",
            "tar",
            "-C",
            remote_dir,
            "-cf",
            "-",
            ".",
        ]
        result = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        log_result = subprocess.CompletedProcess(argv, result.returncode, stdout="", stderr=result.stderr.decode("utf-8", errors="replace"))
        self._append_ssh_log(ssh_log, argv, log_result)
        if result.returncode != 0:
            detail = result.stderr.decode("utf-8", errors="replace").strip()
            raise HarnessError(f"remote log tar download failed: {detail}")
        with tarfile.open(fileobj=io.BytesIO(result.stdout), mode="r:") as tf:
            tf.extractall(local_dir)

    def _remote_python(self, source: str, env: Mapping[str, str]) -> CommandResult:
        """Execute an inline Python program on the remote host."""
        exports = "".join(f"export {key}={shlex.quote(value)}\n" for key, value in env.items())
        script = f"{exports}\npython3 - <<'PY'\n{source}\nPY\n"
        return self._remote_bash(self.report_dir / "bootstrap-ssh.log", script)

    def _remote_bash(self, ssh_log: Path, script: str) -> CommandResult:
        """Run one bash script on the remote Mac and log the full transcript."""
        if self.sudo_password:
            preamble = (
                "export MAC_TUNNEL_SUDO_PASSWORD="
                + shlex.quote(self.sudo_password)
                + "\n"
                + "printf '%s\\n' \"$MAC_TUNNEL_SUDO_PASSWORD\" | sudo -S -p '' -v >/dev/null\n"
            )
            script = preamble + script
        argv = [
            "ssh",
            "-p",
            str(self.ssh_config.port),
            f"{self.ssh_config.user}@{self.ssh_config.host}",
            "bash",
            "-s",
        ]
        result = subprocess.run(argv, input=script, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        command = CommandResult(argv=argv, returncode=result.returncode, stdout=result.stdout, stderr=result.stderr)
        self._append_ssh_log(ssh_log, argv, result, script=script)
        if result.returncode != 0:
            detail = (result.stderr or result.stdout).strip()
            raise HarnessError(f"remote command failed: {detail}")
        return command

    def _redact_text(self, text: str) -> str:
        """Redact secrets before writing command transcripts."""
        if not text:
            return text
        redacted = text
        if self.sudo_password:
            redacted = redacted.replace(self.sudo_password, "***REDACTED***")
        return redacted

    def _append_ssh_log(self, path: Path, argv: Sequence[str], result: subprocess.CompletedProcess[str], script: str | None = None) -> None:
        """Append one structured command transcript to a local log file."""
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8") as handle:
            handle.write(f"$ {' '.join(shlex.quote(part) for part in argv)}\n")
            if script:
                handle.write("<<SCRIPT\n")
                handle.write(self._redact_text(script))
                if not script.endswith("\n"):
                    handle.write("\n")
                handle.write("SCRIPT\n")
            if result.stdout:
                handle.write(self._redact_text(result.stdout))
                if not result.stdout.endswith("\n"):
                    handle.write("\n")
            if result.stderr:
                handle.write("[stderr]\n")
                handle.write(self._redact_text(result.stderr))
                if not result.stderr.endswith("\n"):
                    handle.write("\n")
            handle.write(f"[exit={result.returncode}]\n\n")

    def _write_json(self, path: Path, payload: Mapping[str, Any]) -> None:
        """Serialize JSON with stable formatting."""
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def parse_csv_option(raw: str, allowed: Sequence[str], flag: str) -> list[str]:
    """Parse a comma-separated CLI option and validate its values."""
    chosen = [item.strip() for item in raw.split(",") if item.strip()]
    if not chosen:
        raise argparse.ArgumentTypeError(f"{flag} cannot be empty")
    bad = [item for item in chosen if item not in allowed]
    if bad:
        raise argparse.ArgumentTypeError(f"{flag} has unsupported values: {', '.join(bad)}")
    return chosen


def build_parser() -> argparse.ArgumentParser:
    """Create the CLI parser."""
    parser = argparse.ArgumentParser(description="Run a GPTAdmin Mac tunnel/install smoke matrix over SSH.")
    parser.add_argument("--ssh-user", default="user", help="SSH user for the Mac. Default: user")
    parser.add_argument("--ssh-host", default="localhost", help="SSH host for the Mac. Default: localhost")
    parser.add_argument("--ssh-port", type=int, default=2222, help="SSH port for the Mac. Default: 2222")
    parser.add_argument("--backends", default="cloudflare,ngrok,frp", help="Comma-separated tunnel backends.")
    parser.add_argument("--scopes", default="user,system", help="Comma-separated install scopes.")
    parser.add_argument("--report-dir", default="", help="Optional output directory. Default: logs/mac_tunnel_matrix/<timestamp>")
    parser.add_argument("--keep-remote-temp", action="store_true", help="Keep remote temp files for debugging.")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    """CLI entrypoint."""
    parser = build_parser()
    args = parser.parse_args(argv)
    backends = parse_csv_option(args.backends, DEFAULT_BACKENDS, "--backends")
    scopes = parse_csv_option(args.scopes, DEFAULT_SCOPES, "--scopes")
    if args.report_dir:
        report_dir = Path(args.report_dir).expanduser().resolve()
    else:
        report_dir = (DEFAULT_REPORT_ROOT / time.strftime("%Y%m%d-%H%M%S")).resolve()
    harness = MacTunnelMatrixHarness(
        ssh_config=SSHConfig(user=args.ssh_user, host=args.ssh_host, port=args.ssh_port),
        report_dir=report_dir,
        backends=backends,
        scopes=scopes,
        keep_remote_temp=args.keep_remote_temp,
    )
    return harness.run()


if __name__ == "__main__":
    raise SystemExit(main())
