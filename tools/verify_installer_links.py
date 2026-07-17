#!/usr/bin/env python3
"""Externally verify that the public installer selects and downloads target artifacts.

The verifier starts a temporary HTTP mirror, obtains ``install.sh`` through a URL,
and executes it with an isolated HOME.  A downloaded probe CLI records the package
URLs that the bootstrap passes to it and fetches them from the mirror.  No repository
module is imported, so this validates the same boundary used by a new user.

Examples:
  python3 tools/verify_installer_links.py --target linux/amd64 --target darwin/arm64 --android
  python3 tools/verify_installer_links.py \
    --installer-url https://became.bezrabotnyi.com/install.sh --target linux/amd64
"""

from __future__ import annotations

import argparse
import http.server
import io
import json
import os
import platform
import shlex
import subprocess
import tempfile
import tarfile
import threading
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_INSTALLER = ROOT / "deploy" / "install.sh"
DEFAULT_ANDROID_INSTALLER = ROOT / "deploy" / "install_android.sh"
PROBE_CLI = r'''#!/usr/bin/env python3
"""Record package arguments and fetch every supplied installer artifact."""
import json
import os
import sys
import urllib.request

args = sys.argv[1:]
urls = []
for option in ("--pkg-all", "--pkg-hub", "--pkg-shellmcp"):
    try:
        urls.append(args[args.index(option) + 1])
    except (ValueError, IndexError):
        raise SystemExit("missing installer option: " + option)
for url in urls:
    with urllib.request.urlopen(url, timeout=10) as response:
        response.read()
with open(os.environ["INSTALLER_LINK_PROBE_LOG"], "w", encoding="utf-8") as output:
    json.dump({"args": args, "urls": urls}, output)
'''


@dataclass(frozen=True)
class Target:
    """One target platform represented by installer ``uname`` output."""

    platform: str
    arch: str

    @property
    def name(self) -> str:
        """Return the stable platform/architecture identifier."""
        return f"{self.platform}/{self.arch}"

    @property
    def package_name(self) -> str:
        """Return the all-in-one release asset expected by install.sh."""
        return f"gptadmin-{self.platform}-{self.arch}.tar.gz"

    @property
    def uname_system(self) -> str:
        """Return the OS identifier emitted by uname -s."""
        return "Darwin" if self.platform == "darwin" else self.platform.capitalize()

    @property
    def uname_machine(self) -> str:
        """Return a canonical machine identifier emitted by uname -m."""
        return "arm64" if self.arch == "arm64" else "x86_64"


@dataclass
class RunReport:
    """The externally observed download contract for one target."""

    target: str
    requests: list[str]
    cli_args: list[str]
    package_urls: list[str]

    def to_dict(self) -> dict[str, Any]:
        """Serialize this run for CLI output."""
        return {
            "target": self.target,
            "requests": self.requests,
            "cli_args": self.cli_args,
            "package_urls": self.package_urls,
        }


class Mirror:
    """Temporary release mirror that records each externally requested path."""

    def __init__(self, installer: bytes | None, android_installer: bytes | None) -> None:
        self.installer = installer
        self.android_installer = android_installer
        self.android_package = android_package()
        self.requests: list[str] = []
        self._lock = threading.Lock()
        self._server: http.server.ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None

    def __enter__(self) -> "Mirror":
        mirror = self

        class Handler(http.server.BaseHTTPRequestHandler):
            """Serve only the installer, probe CLI, and deterministic artifacts."""

            def do_GET(self) -> None:  # noqa: N802 - required BaseHTTPRequestHandler method name.
                with mirror._lock:
                    mirror.requests.append(self.path)
                if self.path == "/install.sh" and mirror.installer is not None:
                    self._send(200, "text/x-shellscript", mirror.installer)
                    return
                if self.path == "/install_android.sh" and mirror.android_installer is not None:
                    self._send(200, "text/x-shellscript", mirror.android_installer)
                    return
                if self.path == "/gptadmin.py":
                    self._send(200, "text/x-python", PROBE_CLI.encode("utf-8"))
                    return
                if self.path.startswith("/releases/gptadmin-") and self.path.endswith(".tar.gz"):
                    package = mirror.android_package if self.path == "/releases/gptadmin-android-arm64.tar.gz" else b"installer-link-verifier-artifact\n"
                    self._send(200, "application/gzip", package)
                    return
                self._send(404, "text/plain", b"not found\n")

            def _send(self, status: int, content_type: str, body: bytes) -> None:
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, format: str, *args: object) -> None:
                """Keep verifier output deterministic; requests are retained in memory."""

        self._server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=5)

    @property
    def base_url(self) -> str:
        """Return this mirror's local base URL."""
        assert self._server is not None
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}"

    def snapshot_requests(self) -> list[str]:
        """Return a copy of observed request paths."""
        with self._lock:
            return list(self.requests)


def android_package() -> bytes:
    """Build the smallest valid Android package for the isolated Termux probe."""
    data = io.BytesIO()
    payload = b"#!/usr/bin/env bash\nexit 0\n"
    with tarfile.open(fileobj=data, mode="w:gz") as archive:
        info = tarfile.TarInfo("bin/shellmcp")
        info.mode = 0o755
        info.size = len(payload)
        archive.addfile(info, io.BytesIO(payload))
    return data.getvalue()


def parse_target(raw: str) -> Target:
    """Parse and validate a ``platform/arch`` target identifier."""
    try:
        platform_name, arch = raw.lower().split("/", 1)
    except ValueError as error:
        raise argparse.ArgumentTypeError("target must use platform/arch, for example linux/amd64") from error
    aliases = {"x86_64": "amd64", "aarch64": "arm64"}
    arch = aliases.get(arch, arch)
    if platform_name not in {"linux", "darwin"}:
        raise argparse.ArgumentTypeError(f"unsupported platform {platform_name!r}; expected linux or darwin")
    if arch not in {"amd64", "arm64"}:
        raise argparse.ArgumentTypeError(f"unsupported architecture {arch!r}; expected amd64 or arm64")
    return Target(platform_name, arch)


def host_target() -> Target:
    """Derive the current host's supported target for the default CLI mode."""
    system_name = platform.system().lower()
    if system_name == "darwin":
        system_name = "darwin"
    elif system_name == "linux":
        system_name = "linux"
    else:
        raise RuntimeError(f"unsupported host platform {system_name!r}; pass --target explicitly")
    return parse_target(f"{system_name}/{platform.machine()}")


def command_environment(root: Path, target: Target, mirror_url: str, probe_log: Path) -> dict[str, str]:
    """Build a fully isolated environment for one installer invocation."""
    fakebin = root / "fakebin"
    fakebin.mkdir()
    uname = fakebin / "uname"
    uname.write_text(
        "#!/usr/bin/env bash\n"
        "case \"${1:-}\" in\n"
        f"  -s) printf '%s\\n' {shlex.quote(target.uname_system)} ;;\n"
        f"  -m) printf '%s\\n' {shlex.quote(target.uname_machine)} ;;\n"
        "  *) command /usr/bin/uname \"$@\" ;;\n"
        "esac\n",
        encoding="utf-8",
    )
    uname.chmod(0o755)

    home = root / "home"
    env = os.environ.copy()
    env.update(
        {
            "HOME": str(home),
            "PATH": f"{fakebin}{os.pathsep}{env['PATH']}",
            "BASE_URL": mirror_url,
            "RELEASES_URL": f"{mirror_url}/releases",
            "GPTADMIN_INSTALL_MODE": "user",
            "GPTADMIN_INSTALL_ACTION": "update",
            "GPTADMIN_HOME": str(root / "install"),
            "GPTADMIN_CONFIG_DIR": str(root / "config"),
            "GPTADMIN_CLI_PATH": str(root / "bin" / "gptadmin"),
            "GPTADMIN_DOWNLOAD_QUIET": "1",
            "INSTALLER_LINK_PROBE_LOG": str(probe_log),
            "NO_PROXY": "localhost,127.0.0.1",
            "no_proxy": "localhost,127.0.0.1",
            "HTTP_PROXY": "",
            "HTTPS_PROXY": "",
            "ALL_PROXY": "",
        }
    )
    return env


def verify_target(installer_url: str, mirror: Mirror, target: Target) -> RunReport:
    """Execute one URL-delivered installer and validate its observed downloads."""
    request_offset = len(mirror.snapshot_requests())
    with tempfile.TemporaryDirectory(prefix="gptadmin-installer-link-") as raw_temp:
        temp = Path(raw_temp)
        probe_log = temp / "probe.json"
        env = command_environment(temp, target, mirror.base_url, probe_log)
        command = f"curl -fsSL {shlex.quote(installer_url)} | bash"
        result = subprocess.run(
            ["bash", "-c", command],
            cwd=temp,
            env=env,
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
        )
        if result.returncode != 0:
            raise RuntimeError(
                f"installer failed for {target.name} with rc={result.returncode}\n"
                f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
            )
        if not probe_log.exists():
            raise RuntimeError(f"installer for {target.name} did not execute the downloaded CLI probe")
        probe = json.loads(probe_log.read_text(encoding="utf-8"))
        package_urls = probe.get("urls")
        if not isinstance(package_urls, list):
            raise RuntimeError(f"CLI probe did not record package URLs: {probe!r}")

        expected_urls = [
            f"{mirror.base_url}/releases/{target.package_name}",
            f"{mirror.base_url}/releases/gptadmin-hub.tar.gz",
            f"{mirror.base_url}/releases/gptadmin-shellmcp.tar.gz",
        ]
        if package_urls != expected_urls:
            raise RuntimeError(f"wrong package URLs for {target.name}: got {package_urls!r}, want {expected_urls!r}")
        requests = mirror.snapshot_requests()[request_offset:]
        expected_paths = ["/gptadmin.py", *[urllib.parse.urlparse(url).path for url in expected_urls]]
        missing = [path for path in expected_paths if path not in requests]
        if missing:
            raise RuntimeError(f"installer did not download expected paths for {target.name}: {missing!r}; got {requests!r}")
        args = probe.get("args")
        if not isinstance(args, list) or "update" not in args or "--user" not in args:
            raise RuntimeError(f"CLI invocation lost install action or mode for {target.name}: {args!r}")
        return RunReport(target.name, requests, args, package_urls)


def verify_android(installer_url: str, mirror: Mirror) -> RunReport:
    """Execute the Termux installer and verify its Android ARM64 package download."""
    request_offset = len(mirror.snapshot_requests())
    with tempfile.TemporaryDirectory(prefix="gptadmin-android-installer-link-") as raw_temp:
        temp = Path(raw_temp)
        env = command_environment(temp, Target("linux", "amd64"), mirror.base_url, temp / "unused-probe.json")
        prefix = temp / "termux-prefix"
        (prefix / "bin").mkdir(parents=True)
        (prefix / "var" / "service").mkdir(parents=True)
        package_url = f"{mirror.base_url}/releases/gptadmin-android-arm64.tar.gz"
        env.update(
            {
                "PREFIX": str(prefix),
                "PACKAGE_URL": package_url,
                "GPTADMIN_DIR": str(temp / "gptadmin"),
                "GPTADMIN_CONFIG_DIR": str(temp / "config"),
                "BIN_DIR": str(prefix / "bin"),
                "HUB_URL": "https://hub.example.test",
                "SHELLMCP_TOKEN": "android-contract-token",
                "SHELLMCP_AUTO_START": "0",
                "SHELLMCP_FOREGROUND": "0",
                "SHELLMCP_ANDROID_PRIVILEGE": "none",
            }
        )
        command = f"curl -fsSL {shlex.quote(installer_url)} | bash"
        result = subprocess.run(
            ["bash", "-c", command],
            cwd=temp,
            env=env,
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
        )
        if result.returncode != 0:
            raise RuntimeError(
                f"Android installer failed with rc={result.returncode}\n"
                f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
            )
        installed = prefix / "bin" / "gptadmin-shellmcp"
        if not installed.is_file() or not os.access(installed, os.X_OK):
            raise RuntimeError(f"Android installer did not install executable {installed}")
        env_file = temp / "config" / "shellmcp.env"
        env_text = env_file.read_text(encoding="utf-8") if env_file.is_file() else ""
        if "SHELLMCP_MODE=long_poll" not in env_text or "SHELLMCP_QUEUE=1" not in env_text:
            raise RuntimeError(f"Android installer wrote an invalid polling configuration: {env_text!r}")
        requests = mirror.snapshot_requests()[request_offset:]
        asset_path = urllib.parse.urlparse(package_url).path
        if asset_path not in requests:
            raise RuntimeError(f"Android installer did not download {asset_path!r}; got {requests!r}")
        return RunReport("android/arm64", requests, [], [package_url])


def build_parser() -> argparse.ArgumentParser:
    """Build the command-line parser for the installer-link verifier."""
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--installer", type=Path, default=DEFAULT_INSTALLER, help="local install.sh to serve through the temporary HTTP mirror")
    source.add_argument("--installer-url", help="external install.sh URL to fetch with curl before execution")
    android_source = parser.add_mutually_exclusive_group()
    android_source.add_argument("--android-installer", type=Path, default=DEFAULT_ANDROID_INSTALLER, help="local install_android.sh to serve through the temporary HTTP mirror")
    android_source.add_argument("--android-installer-url", help="external install_android.sh URL to fetch with curl before execution")
    parser.add_argument("--target", action="append", type=parse_target, help="target platform/arch; repeatable, default is the current host")
    parser.add_argument("--android", action="store_true", help="also execute the Android/Termux ARM64 installer contract")
    parser.add_argument("--json", action="store_true", help="print a JSON report")
    return parser


def main(argv: list[str] | None = None) -> int:
    """Run verifier targets and print the externally observed installer contract."""
    args = build_parser().parse_args(argv)
    installer_path: Path | None = args.installer
    installer_data: bytes | None = None
    android_installer_data: bytes | None = None
    installer_url = args.installer_url
    if installer_url is None:
        assert installer_path is not None
        if not installer_path.is_file():
            raise SystemExit(f"installer does not exist: {installer_path}")
        installer_data = installer_path.read_bytes()
    android_installer_url = args.android_installer_url
    if args.android and android_installer_url is None:
        android_installer_path: Path = args.android_installer
        if not android_installer_path.is_file():
            raise SystemExit(f"Android installer does not exist: {android_installer_path}")
        android_installer_data = android_installer_path.read_bytes()
    targets: list[Target] = args.target or [host_target()]

    with Mirror(installer_data, android_installer_data) as mirror:
        selected_installer_url = installer_url or f"{mirror.base_url}/install.sh"
        try:
            runs = [verify_target(selected_installer_url, mirror, target) for target in targets]
            if args.android:
                selected_android_url = android_installer_url or f"{mirror.base_url}/install_android.sh"
                runs.append(verify_android(selected_android_url, mirror))
        except RuntimeError as error:
            print(f"FAIL: {error}", file=os.sys.stderr)
            return 1
    report = {"ok": True, "runs": [run.to_dict() for run in runs]}
    if args.json:
        print(json.dumps(report, sort_keys=True))
    else:
        for run in runs:
            print(f"OK {run.target}: downloaded {', '.join(run.requests)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
