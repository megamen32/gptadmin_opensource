#!/usr/bin/env python3
"""Run a disposable real-Hub canary, reconnect and rollback drill.

This is process-level evidence for S3.5. It does not publish artifacts or
replace a clean-host signed release canary; it verifies that the Hub endpoint,
safe MCP client path and rollback transaction behave across a binary swap.
"""

from __future__ import annotations

import importlib.util
import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
GO_HUB = ROOT / "go-hub"
LIVE_RUNNER_PATH = Path(__file__).with_name("live_acceptance.py")
LIVE_SPEC = importlib.util.spec_from_file_location("live_acceptance", LIVE_RUNNER_PATH)
if LIVE_SPEC is None or LIVE_SPEC.loader is None:
    raise RuntimeError("cannot load live acceptance runner")
live_acceptance = importlib.util.module_from_spec(LIVE_SPEC)
LIVE_SPEC.loader.exec_module(live_acceptance)


class CanaryError(RuntimeError):
    """Raised for a failed disposable canary without retaining service output."""


def _free_port() -> int:
    """Return an unused loopback port for the disposable Hub."""

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _build(binary: Path, version: str) -> None:
    """Build one actual Go Hub binary with an observable canary version."""

    ldflags = (
        "-X github.com/megamen32/gptadmin/go-hub/internal/hub.BuildVersion="
        f"{version} -X github.com/megamen32/gptadmin/go-hub/internal/hub.GitCommit=canary-test"
    )
    subprocess.run(
        ["go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", str(binary), "./cmd/gptadmin-hub"],
        cwd=GO_HUB,
        check=True,
        timeout=120,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def _version(base_url: str) -> str:
    """Read the non-sensitive build version from `/version`."""

    try:
        with urllib.request.urlopen(base_url.rstrip("/") + "/version", timeout=5) as response:
            payload = json.loads(response.read().decode("utf-8"))
    except (OSError, urllib.error.URLError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise CanaryError(f"version probe failed ({type(exc).__name__})") from None
    value = payload.get("build_version") if isinstance(payload, dict) else None
    if not isinstance(value, str) or not value:
        raise CanaryError("version probe did not return build_version")
    return value


@dataclass
class _HubProcess:
    """A disposable process and its stable MCP endpoint."""

    process: subprocess.Popen[bytes]
    base_url: str

    @classmethod
    def start(cls, binary: Path, port: int, config_dir: Path) -> "_HubProcess":
        """Start one candidate and wait for its real `/version` endpoint."""

        env = os.environ.copy()
        env.update(
            {
                "GPTADMIN_HUB_HOST": "127.0.0.1",
                "GPTADMIN_HUB_PORT": str(port),
                "HUB_HOST": "127.0.0.1",
                "HUB_PORT": str(port),
                "PORT": str(port),
                "CTL_TOKEN": "ctl",
                "ADMIN_PASSWORD": "pw",
                "PUBLIC_ORIGIN": f"http://127.0.0.1:{port}",
                "MCP_RESOURCE": f"http://127.0.0.1:{port}",
                "GPTADMIN_CONFIG_DIR": str(config_dir),
                "GPTADMIN_ROOT": str(ROOT),
                "NO_PROXY": "localhost,127.0.0.1",
                "no_proxy": "localhost,127.0.0.1",
                "HTTP_PROXY": "",
                "HTTPS_PROXY": "",
                "ALL_PROXY": "",
            }
        )
        try:
            process = subprocess.Popen(
                [str(binary)],
                cwd=ROOT,
                env=env,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
        except OSError as exc:
            raise CanaryError(f"candidate could not start ({type(exc).__name__})") from None
        candidate = cls(process, f"http://127.0.0.1:{port}")
        try:
            candidate.wait_ready()
        except Exception:
            candidate.stop()
            raise
        return candidate

    def wait_ready(self) -> None:
        """Wait for a live version response or fail without logging service output."""

        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise CanaryError("candidate exited before readiness")
            try:
                with urllib.request.urlopen(self.base_url + "/version", timeout=1) as response:
                    if response.status == 200:
                        return
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        raise CanaryError("candidate did not become ready")

    def stop(self) -> None:
        """Stop the candidate's process group and wait for cleanup."""

        if self.process.poll() is not None:
            return
        try:
            os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
            self.process.wait(timeout=5)
        except (ProcessLookupError, subprocess.TimeoutExpired):
            if self.process.poll() is None:
                os.killpg(os.getpgid(self.process.pid), signal.SIGKILL)
                self.process.wait(timeout=5)


def _assert_candidate(hub: _HubProcess, expected_version: str) -> None:
    """Run version and the complete safe live smoke against one candidate."""

    if _version(hub.base_url) != expected_version:
        raise CanaryError(f"candidate version mismatch: expected {expected_version}")
    live_acceptance.run_acceptance(hub.base_url, "ctl", required_tools={"demo"})


def run_disposable_canary() -> dict[str, Any]:
    """Build old/new candidates, reconnect across swap and rollback a bad one."""

    with tempfile.TemporaryDirectory(prefix="gptadmin-canary-") as raw_root:
        root = Path(raw_root)
        old_binary = root / "hub-old"
        new_binary = root / "hub-new"
        bad_binary = root / "hub-bad"
        config_dir = root / "config"
        config_dir.mkdir()
        _build(old_binary, "canary-old")
        _build(new_binary, "canary-new")
        bad_binary.write_bytes(b"not a Go executable")
        bad_binary.chmod(0o755)
        port = _free_port()
        hub: _HubProcess | None = None
        try:
            hub = _HubProcess.start(old_binary, port, config_dir)
            _assert_candidate(hub, "canary-old")
            hub.stop()
            hub = _HubProcess.start(new_binary, port, config_dir)
            _assert_candidate(hub, "canary-new")
            hub.stop()
            try:
                hub = _HubProcess.start(bad_binary, port, config_dir)
                _assert_candidate(hub, "canary-new")
            except CanaryError:
                if hub is not None:
                    hub.stop()
                hub = _HubProcess.start(new_binary, port, config_dir)
                _assert_candidate(hub, "canary-new")
            finally:
                if hub is not None:
                    hub.stop()
        finally:
            if hub is not None:
                hub.stop()
    return {
        "status": "passed",
        "old_version": "canary-old",
        "new_version": "canary-new",
        "reconnected": True,
        "rollback": True,
    }


def main() -> int:
    """Run the drill and print only its redacted summary."""

    try:
        result = run_disposable_canary()
    except CanaryError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())
