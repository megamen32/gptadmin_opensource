#!/usr/bin/env python3
"""Controlled Chrome profile manager MCP.

Linux and macOS controlled profiles, with separately managed extensions.
Ordinary Chrome profiles are discoverable but unavailable. Backups cover the
extension artifact, not Chrome profile data or extension storage.
"""

from __future__ import annotations

import hashlib
import base64
import re
import json
import os
import platform
import shutil
import sys
import tempfile
try:
    import fcntl
except ImportError:
    fcntl = None
import subprocess
import threading
import queue
import uuid
import stat
import copy
import socket
import struct
from contextlib import contextmanager
from pathlib import Path
from typing import Any


class ProfileManagerError(RuntimeError):
    pass


def bridge_call(profile_id: str, method: str, extension_id: str | None = None) -> Any:
    """Call a previously paired native bridge without exposing its capability."""
    if not isinstance(profile_id, str) or not profile_id or len(profile_id) > 128 or any(ord(ch) < 32 or ord(ch) == 127 for ch in profile_id):
        raise ProfileManagerError("invalid profile identifier")
    if method not in {"list", "get", "enable", "disable"}:
        raise ProfileManagerError("unsupported bridge operation")
    if method != "list" and not re.fullmatch(r"[a-p]{32}", extension_id or ""):
        raise ProfileManagerError("invalid extension id")
    config_dir = Path(os.environ.get("XDG_CONFIG_HOME", str(Path.home() / ".config"))) / "gptadmin-extension-bridge"
    def read_private(name):
        path = config_dir / name
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "r", encoding="utf-8") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
                raise ProfileManagerError("unsafe bridge registry")
            return json.load(stream)
    try:
        bindings = read_private("bindings.json")
        registry = read_private("profiles.json")
        if not isinstance(bindings, dict) or not isinstance(registry, dict):
            raise ProfileManagerError("invalid bridge registry")
        origin = bindings[profile_id]
        if not isinstance(origin, str) or not re.fullmatch(r"chrome-extension://[a-p]{32}/", origin):
            raise ProfileManagerError("invalid bridge binding")
        if method == "disable" and extension_id == origin[len("chrome-extension://"):-1]:
            raise ProfileManagerError("cannot disable its own bridge")
        if sum(value == origin for value in bindings.values()) != 1:
            raise ProfileManagerError("ambiguous bridge binding")
        secret = registry[origin]
        if not isinstance(secret, str) or not re.fullmatch(r"[a-f0-9]{64}", secret):
            raise ProfileManagerError("invalid bridge capability")
        path = f"/tmp/gptadmin-extension-{os.getuid()}/{secret[:32]}.sock"
        request = json.dumps({"secret": secret, "method": method, "extension_id": extension_id}, separators=(",", ":")).encode()
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(30)
            conn.connect(path)
            conn.sendall(struct.pack("=I", len(request)) + request)
            def read_exact(n):
                chunks = bytearray()
                while len(chunks) < n:
                    data = conn.recv(n - len(chunks))
                    if not data: raise ProfileManagerError("bridge disconnected; outcome unknown")
                    chunks.extend(data)
                return bytes(chunks)
            length = struct.unpack("=I", read_exact(4))[0]
            if length > 1024 * 1024: raise ProfileManagerError("bridge response too large")
            response = json.loads(read_exact(length))
        if not isinstance(response, dict): raise ProfileManagerError("invalid bridge response")
        if response.get("outcome") == "unknown":
            raise ProfileManagerError("bridge operation outcome unknown; read back before retry")
        if "error" in response: raise ProfileManagerError("Chrome rejected bridge operation: " + str(response["error"]))
        if "result" not in response: raise ProfileManagerError("invalid bridge response")
        result = response["result"]
        if method == "list" and not isinstance(result, list):
            raise ProfileManagerError("invalid bridge list response")
        if method != "list":
            if not isinstance(result, dict) or result.get("id") != extension_id:
                raise ProfileManagerError("bridge readback mismatch; outcome unknown")
            if method in {"enable", "disable"} and result.get("enabled") is not (method == "enable"):
                raise ProfileManagerError("bridge state mismatch; outcome unknown")
        return result
    except (OSError, ValueError, KeyError, TypeError) as exc:
        raise ProfileManagerError("bridge unavailable or operation outcome unknown") from exc


def platform_lifecycle_status() -> tuple[bool, str]:
    """Fail closed when the tested lock and artifact-copy primitives are absent."""
    system = platform.system()
    if system not in ("Linux", "Darwin"):
        return False, "platform_lifecycle_not_verified"
    if fcntl is None or not hasattr(fcntl, "flock"):
        return False, "interprocess_lock_unavailable"
    if not all(hasattr(os, name) for name in ("O_DIRECTORY", "O_NOFOLLOW", "O_NONBLOCK")):
        return False, "safe_artifact_copy_unavailable"
    if not all(fn in os.supports_dir_fd for fn in (os.open, os.stat)):
        return False, "safe_artifact_copy_unavailable"
    if os.listdir not in os.supports_fd or os.stat not in os.supports_follow_symlinks:
        return False, "safe_artifact_copy_unavailable"
    if os.scandir not in os.supports_fd:
        return False, "safe_artifact_copy_unavailable"
    return True, system.lower() + "_primitives_present"


def require_lifecycle_platform() -> None:
    supported, reason = platform_lifecycle_status()
    if not supported: raise ProfileManagerError("platform lifecycle unavailable: " + reason)


def backend_configuration() -> dict[str, Any]:
    """Report configuration evidence only; never claim a running Chrome."""
    entry = os.environ.get("CHROME_DEVTOOLS_MCP_ENTRY", "")
    result = {"package_metadata": "not_configured", "node": "present" if shutil.which("node") else "missing", "runtime": "not_probed", "readback_adapter": "derived_pinned_1.12.0_cache_fix"}
    if not entry: return result
    try:
        path = Path(entry).resolve(strict=True)
        if path.name != "chrome-devtools-mcp.js" or not path.is_file(): raise ValueError()
        info = json.loads((path.parents[3] / "package.json").read_text(encoding="utf-8"))
        if not isinstance(info, dict) or info.get("name") != "chrome-devtools-mcp" or info.get("version") != "1.12.0": raise ValueError()
        result["package_metadata"] = "matched_1.12.0"
    except (OSError, ValueError, IndexError, TypeError):
        result["package_metadata"] = "invalid_or_unavailable"
    return result


def lifecycle_transition(state: dict[str, Any], request: dict[str, Any]) -> dict[str, Any]:
    """Pure decision engine: return next durable intent and ordered effect descriptions."""
    new = copy.deepcopy(state)
    extensions = new["extensions"]
    operation = request["operation"]
    target = copy.deepcopy(request.get("target"))
    extension_id = target["extension_id"] if target is not None else request.get("extension_id")
    if extension_id is None:
        if len(extensions) != 1: raise ProfileManagerError("extension_id required for this profile")
        extension_id = next(iter(extensions))
    if not isinstance(extension_id, str) or not re.fullmatch(r"[a-p]{32}", extension_id):
        raise ProfileManagerError("invalid extension_id")
    entry = extensions.get(extension_id)
    if operation in ("install", "update", "rollback"):
        if not target: raise ProfileManagerError("target required")
        if request.get("extension_id") not in (None, extension_id): raise ProfileManagerError("backup extension identity mismatch")
        if entry and entry.get("pending"): raise ProfileManagerError("pending operation requires readback recovery")
        current = entry.get("current") if entry else None
        if (current["version"] if current else None) != request.get("expected_version"):
            raise ProfileManagerError("compare-and-swap version mismatch")
        if operation in ("update", "rollback") and not current: raise ProfileManagerError("controlled extension unavailable")
        if current and current["version"] == target["version"]:
            raise ProfileManagerError("update requires a distinct version for verifiable readback")
        if entry is None:
            path = "extension" if not extensions else "extensions/" + extension_id + "/extension"
            entry = {"current": None, "pending": None, "path": path}
            extensions[extension_id] = entry
        entry["pending"] = target
        entry["phase"] = "before_activate"
        effects = ([{"kind": "verify_readback", "item": current}] if current else [{"kind": "ensure_absent", "extension_id": extension_id}])
        effects += [{"kind":"prepare", "item":target, "path":entry["path"]}, {"kind":"persist"},
                    {"kind":"activate", "path":entry["path"], "extension_id":extension_id},
                    {"kind":"set_phase", "extension_id":extension_id, "phase":"before_mutation"},
                    {"kind":"set_phase", "extension_id":extension_id, "phase":"mutation_unknown"},
                    {"kind":"mutate", "action":"reload_extension" if current else "install_extension", "item":target, "path":entry["path"]},
                    {"kind":"verify_readback", "item":target}, {"kind":"finish", "extension_id":extension_id}]
        return {"state":new, "effects":effects, "result":target}
    if entry is None: raise ProfileManagerError("controlled extension unavailable")
    current = entry.get("current"); pending = entry.get("pending")
    if operation == "restore":
        if not current or pending:
            return {"state":new, "effects":[], "result":None}
        matches = [x for x in request["observed"] if x.get("id") == extension_id]
        if matches:
            if len(matches) != 1 or matches[0].get("version") != current["version"] or matches[0].get("enabled") is not True:
                raise ProfileManagerError("extension readback mismatch")
            return {"state":new, "effects":[], "result":current}
        entry["pending"] = current
        entry["phase"] = "before_mutation"
        effects = [{"kind":"verify_active", "item":current, "path":entry["path"]}, {"kind":"persist"},
                   {"kind":"set_phase", "extension_id":extension_id, "phase":"mutation_unknown"},
                   {"kind":"mutate", "action":"install_extension", "item":current, "path":entry["path"]},
                   {"kind":"verify_readback", "item":current}, {"kind":"finish", "extension_id":extension_id}]
        return {"state":new, "effects":effects, "result":current}
    if operation == "complete":
        if not pending: raise ProfileManagerError("no pending operation")
        if entry.get("phase", "mutation_unknown") != "mutation_unknown": raise ProfileManagerError("mutation not yet attempted")
        if pending.get("operation") == "reload": entry["pending"] = None
        else: entry["current"] = pending; entry["pending"] = None
        entry.pop("phase", None)
        return {"state":new, "effects":[{"kind":"persist"}], "result":entry["current"]}
    if operation == "set_phase":
        if not pending: raise ProfileManagerError("no pending operation")
        old_phase = entry.get("phase", "mutation_unknown")
        next_phase = request["phase"]
        if (old_phase, next_phase) not in (("before_activate", "before_mutation"), ("before_mutation", "mutation_unknown")):
            raise ProfileManagerError("invalid pending phase transition")
        entry["phase"] = next_phase
        return {"state":new, "effects":[{"kind":"persist"}], "result":None}
    if operation == "reconcile":
        if not pending: return {"state":new, "effects":[], "result":{"extension_id":extension_id, "status":"no_pending_operation"}}
        phase = entry.get("phase", "mutation_unknown")
        if phase in ("before_activate", "before_mutation"):
            effect = {"kind":"restore_pre_mutation", "item":current, "target":pending, "path":entry["path"], "extension_id":extension_id}
            if current:
                entry["pending"] = None; entry.pop("phase", None)
            else:
                del extensions[extension_id]
            return {"state":new, "effects":[effect, {"kind":"persist"}], "result":{"extension_id":extension_id, "status":"pre_mutation_recovered", "version":current["version"] if current else None}}
        if phase != "mutation_unknown": raise ProfileManagerError("unknown pending phase")
        effects = [{"kind":"verify_readback", "item":pending}, {"kind":"verify_active", "item":pending, "path":entry["path"]}]
        effects.append({"kind":"unprovable_reload"} if pending.get("operation") == "reload" else {"kind":"finish", "extension_id":extension_id})
        return {"state":new, "effects":effects, "result":pending}
    if pending: raise ProfileManagerError("pending operation requires readback recovery")
    if (current["version"] if current else None) != request.get("expected_version"):
        raise ProfileManagerError("compare-and-swap version mismatch")
    if not current: raise ProfileManagerError("controlled extension unavailable")
    checks = [{"kind":"verify_readback", "item":current}, {"kind":"verify_active", "item":current, "path":entry["path"]}]
    if operation == "backup":
        return {"state":new, "effects":checks + [{"kind":"verify_stage", "item":current}, {"kind":"write_backup", "item":current, "backup_id":request["backup_id"]}], "result":current}
    if operation == "reload":
        entry["pending"] = {**current, "operation":"reload"}
        entry["phase"] = "before_mutation"
        effects = checks + [{"kind":"persist"}, {"kind":"set_phase", "extension_id":extension_id, "phase":"mutation_unknown"}, {"kind":"mutate", "action":"reload_extension", "item":current, "reload":True},
                            {"kind":"verify_readback", "item":current}, {"kind":"finish", "extension_id":extension_id}]
        return {"state":new, "effects":effects, "result":current}
    raise ProfileManagerError("unknown lifecycle operation")


def _safe_id(value: str) -> str:
    if not value or value in (".", "..") or len(value) > 128 or any(ch not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-" for ch in value):
        raise ProfileManagerError("invalid identifier")
    return value


def extension_identity(manifest: dict[str, Any]) -> tuple[str, str]:
    try:
        key = base64.b64decode(manifest["key"], validate=True)
        version = manifest["version"]
        if not key or not isinstance(version, str) or not re.fullmatch(r"[0-9]+(?:\.[0-9]+){0,3}", version):
            raise ValueError("invalid manifest")
        name = manifest["name"]
        if not isinstance(name, str) or not name or any(ord(c) < 32 or ord(c) == 127 or c == '"' for c in name):
            raise ValueError("unsafe extension name")
    except (KeyError, TypeError, ValueError) as exc:
        raise ProfileManagerError("extension requires stable key and version") from exc
    raw = hashlib.sha256(key).digest()[:16]
    return "".join(chr(97 + nibble) for byte in raw for nibble in (byte >> 4, byte & 15)), version


def content_hash(root: Path) -> str:
    digest = hashlib.sha256()
    size = count = 0
    for item in sorted(root.rglob("*")):
        if item.is_symlink() or not (item.is_file() or item.is_dir()):
            raise ProfileManagerError("symlinks and special files are forbidden")
        if item.is_file():
            count += 1
            file_size = item.stat().st_size
            size += file_size
            if count > 10000 or size > 200 * 1024 * 1024:
                raise ProfileManagerError("extension exceeds staging limits")
            name = item.relative_to(root).as_posix().encode("utf-8")
            digest.update(len(name).to_bytes(8, "big")); digest.update(name)
            digest.update(file_size.to_bytes(8, "big"))
            with item.open("rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
    return digest.hexdigest()


MAX_ARTIFACT_ENTRIES = 10000
MAX_ARTIFACT_FILES = 10000
MAX_ARTIFACT_BYTES = 200 * 1024 * 1024
MAX_ARTIFACT_DEPTH = 64


def copy_artifact(root: Path, relative: str, destination: Path) -> None:
    """Copy via directory descriptors; never follow a source symlink."""
    require_lifecycle_platform()
    parts = Path(relative).parts
    if not parts or Path(relative).is_absolute() or any(x in (".", "..") for x in parts):
        raise ProfileManagerError("artifact path must be relative to artifacts root")
    if len(parts) > MAX_ARTIFACT_DEPTH:
        raise ProfileManagerError("artifact path exceeds depth limit")
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    descriptors = []
    try:
        fd = os.open(root, flags); descriptors.append(fd)
        for part in parts:
            fd = os.open(part, flags, dir_fd=fd); descriptors.append(fd)
        def visit(directory: int, target: Path | None, depth: int, total: list[int]) -> None:
            if target is not None:
                target.mkdir()
            with os.scandir(directory) as iterator:
                for child_entry in iterator:
                    name = child_entry.name
                    total[0] += 1
                    if total[0] > MAX_ARTIFACT_ENTRIES:
                        raise ProfileManagerError("extension exceeds staging entries limit")
                    if depth + 1 > MAX_ARTIFACT_DEPTH:
                        raise ProfileManagerError("extension exceeds staging depth limit")
                    entry = os.stat(name, dir_fd=directory, follow_symlinks=False)
                    if stat.S_ISDIR(entry.st_mode):
                        child = os.open(name, flags, dir_fd=directory)
                        try: visit(child, target / name if target is not None else None, depth + 1, total)
                        finally: os.close(child)
                    elif stat.S_ISREG(entry.st_mode):
                        child = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
                        try:
                            st = os.fstat(child)
                            if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1: raise ProfileManagerError("invalid or hard-linked artifact file")
                            total[1] += 1; total[2] += st.st_size
                            if total[1] > MAX_ARTIFACT_FILES or total[2] > MAX_ARTIFACT_BYTES:
                                raise ProfileManagerError("extension exceeds staging limits")
                            if target is None:
                                continue
                            with os.fdopen(os.dup(child), "rb") as src, (target / name).open("xb") as dst:
                                left = st.st_size
                                while left:
                                    block = src.read(min(left, 1024 * 1024))
                                    if not block: raise ProfileManagerError("artifact changed while reading")
                                    dst.write(block); left -= len(block)
                                if src.read(1): raise ProfileManagerError("artifact changed while reading")
                            if (target / name).stat().st_size != st.st_size:
                                raise ProfileManagerError("artifact changed while reading")
                        finally: os.close(child)
                    else: raise ProfileManagerError("symlinks and special files are forbidden")
        visit(fd, None, 0, [0, 0, 0])
        visit(fd, destination, 0, [0, 0, 0])
    except RecursionError as exc:
        raise ProfileManagerError("extension exceeds staging depth limit") from exc
    except OSError as exc:
        raise ProfileManagerError("artifact unavailable or unsafe") from exc
    finally:
        for fd in reversed(descriptors): os.close(fd)


class Backend:
    """Run a derived 1.12.0 MCP: refresh CdpExtension objects on every CDP list.

    The private temporary package is copied from the configured installation.
    Its bundled Puppeteer file must match the exact pinned SHA-256 before the
    single cache patch is applied; the original installation is never modified.
    """
    def __init__(self, root: Path):
        require_lifecycle_platform()
        entry = os.environ.get("CHROME_DEVTOOLS_MCP_ENTRY", "")
        path = Path(entry).resolve() if entry else None
        try:
            if not path or path.name != "chrome-devtools-mcp.js": raise ValueError()
            info = json.loads((path.parents[3] / "package.json").read_text())
            if info.get("name") != "chrome-devtools-mcp" or info.get("version") != "1.12.0" or info.get("dependencies", {}): raise ValueError()
        except (ValueError, OSError, IndexError, TypeError, AttributeError) as exc:
            raise ProfileManagerError("official chrome-devtools-mcp 1.12.0 unavailable") from exc
        bundle = path.parents[1] / "third_party/index.js"
        old = b"""            if (this.#extensions.has(currExtension.id)) {
                extensionsMap.set(currExtension.id, this.#extensions.get(currExtension.id));
            }
            else {
                const newExtension = new CdpExtension(currExtension.id, currExtension.version, currExtension.name, currExtension.path, currExtension.enabled, this, this.logger);
                extensionsMap.set(currExtension.id, newExtension);
            }"""
        new = b"""            const newExtension = new CdpExtension(currExtension.id, currExtension.version, currExtension.name, currExtension.path, currExtension.enabled, this, this.logger);
            extensionsMap.set(currExtension.id, newExtension);"""
        try:
            source = bundle.read_bytes()
            if hashlib.sha256(source).hexdigest() != "e1752f75684b672362dc3ae77a399407c509b76471fca08f967fc44fe2cb267b" or source.count(old) != 1:
                raise ValueError("unexpected bundle")
            self.package_stage = tempfile.TemporaryDirectory(prefix="gptadmin-chrome-mcp-")
            package = path.parents[3]
            staged = Path(self.package_stage.name) / "node_modules" / package.name
            staged.parent.mkdir()
            shutil.copytree(package, staged)
            (staged / bundle.relative_to(package)).write_bytes(source.replace(old, new, 1))
            derived_entry = staged / path.relative_to(package)
        except (OSError, ValueError) as exc:
            if hasattr(self, "package_stage"): self.package_stage.cleanup()
            raise ProfileManagerError("chrome-devtools-mcp 1.12.0 bundle mismatch or staging failed") from exc
        root.mkdir(parents=True, exist_ok=True, mode=0o700)
        cmd = ["node", str(derived_entry), "--headless", "--categoryExtensions", "--experimentalStructuredContent", "--no-usage-statistics", "--userDataDir=" + str(root / "chrome"), "--filesystemRoot=" + str(root)]
        try:
            self.proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1)
        except OSError as exc:
            self.package_stage.cleanup()
            raise ProfileManagerError("backend could not start") from exc
        self.messages = queue.Queue(); self.seq = 0
        def reader():
            for line in self.proc.stdout:
                self.messages.put(line)
            self.messages.put(None)
        threading.Thread(target=reader, daemon=True).start()
        try:
            self.call("initialize", {"protocolVersion":"2025-06-18", "capabilities":{}, "clientInfo":{"name":"profile-manager", "version":"1"}})
            self.send({"jsonrpc":"2.0", "method":"notifications/initialized"})
        except Exception:
            self.close(); raise

    def send(self, data):
        try:
            self.proc.stdin.write(json.dumps(data) + "\n"); self.proc.stdin.flush()
        except (OSError, BrokenPipeError) as exc: raise ProfileManagerError("backend disconnected; outcome unknown") from exc

    def call(self, method, params):
        self.seq += 1; number = self.seq
        self.send({"jsonrpc":"2.0", "id":number, "method":method, "params":params})
        try:
            while True:
                line = self.messages.get(timeout=30)
                if line is None: raise ProfileManagerError("backend disconnected; outcome unknown")
                msg = json.loads(line)
                if msg.get("id") != number: continue
                if "error" in msg or msg.get("result", {}).get("isError"):
                    raise ProfileManagerError("backend rejected operation")
                return msg.get("result", {})
        except queue.Empty as exc: raise ProfileManagerError("backend timeout; outcome unknown") from exc
        except ValueError as exc: raise ProfileManagerError("invalid backend response; outcome unknown") from exc

    def tool(self, name, arguments):
        response = self.call("tools/call", {"name":name, "arguments":arguments})
        return "\n".join(item.get("text", "") for item in response.get("content", []) if item.get("type") == "text")

    def close(self):
        if self.proc.poll() is None:
            self.proc.terminate()
            try: self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired: self.proc.kill(); self.proc.wait()
        self.package_stage.cleanup()


class ProfileManager:
    def __init__(self, state_dir: str | Path | None = None, roots: list[str | Path] | None = None, backend_factory=Backend):
        self.state_dir = Path(state_dir or os.environ.get("CHROME_PROFILE_MANAGER_STATE", Path.home() / ".local/state/chrome-profile-manager")).expanduser().resolve()
        self.state_file = self.state_dir / "state.json"
        self.stage_dir = self.state_dir / "stages"
        self.backup_dir = self.state_dir / "backups"
        self.roots = [Path(item).expanduser() for item in (roots if roots is not None else self._default_roots())]
        self.state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.artifact_dir = self.state_dir / "artifacts"
        self.artifact_dir.mkdir(exist_ok=True, mode=0o700)
        self.backend_factory = backend_factory
        self.backends = {}
        self.backend_locks = {}
        self._deferred_restore = set()
        self._restoring = False
        self.helpers = {}
        self.helper_verified = False

    def _default_roots(self) -> list[str]:
        configured = os.environ.get("CHROME_PROFILE_ROOTS", "")
        if configured:
            return [item for item in configured.split(os.pathsep) if item]
        home = Path.home()
        system = platform.system()
        if system == "Darwin":
            return [str(home / "Library/Application Support/Google/Chrome"), str(home / "Library/Application Support/Chromium")]
        if system == "Windows":
            local = Path(os.environ.get("LOCALAPPDATA", home))
            return [str(local / "Google/Chrome/User Data"), str(local / "Chromium/User Data")]
        if system == "Linux":
            config = Path(os.environ.get("XDG_CONFIG_HOME", home / ".config"))
            return [str(config / "google-chrome"), str(config / "chromium")]
        return []

    def capabilities(self) -> dict[str, Any]:
        supported, reason = platform_lifecycle_status()
        backend = backend_configuration()
        eligible = supported and backend["package_metadata"] == "matched_1.12.0" and backend["node"] == "present"
        processes = [child for child in self.backends.values() if hasattr(child, "proc")]
        verified = any(getattr(child, "runtime_verified", False) and child.proc.poll() is None for child in processes)
        if verified: runtime = "verified_current_session"
        elif any(child.proc.poll() is None for child in processes): runtime = "started_unverified"
        elif processes: runtime = "stopped"
        else: runtime = "not_started"
        backend["runtime"] = runtime
        if not supported: controlled_reason = reason
        elif backend["package_metadata"] != "matched_1.12.0": controlled_reason = "backend_" + backend["package_metadata"]
        elif backend["node"] != "present": controlled_reason = "node_missing"
        else: controlled_reason = "runtime_verified" if verified else "runtime_" + runtime
        return {"platform": platform.system(), "backend": backend, "modes": {
            "controlled": {"available": bool(supported and eligible and verified), "eligible_to_attempt": eligible, "reason": controlled_reason},
            "helper": {"available": False, "reason": "current_availability_not_probed", "reload_verified_this_session": self.helper_verified},
            "policy": {"available": False, "reason": "not_implemented"},
            "unmanaged_unavailable": {"available": False, "reason": "ordinary_profiles_not_managed"},
        }}

    def _read_state(self) -> dict[str, Any]:
        if not self.state_file.exists():
            return {"controlled": {}, "backups": {}}
        try:
            value = json.loads(self.state_file.read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise ProfileManagerError("state is unreadable") from exc
        if not isinstance(value, dict) or not isinstance(value.get("controlled", {}), dict):
            raise ProfileManagerError("state is invalid")
        value.setdefault("backups", {})
        return value

    def _write_state(self, value: dict[str, Any]) -> None:
        fd, name = tempfile.mkstemp(prefix="state.", dir=self.state_dir)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                json.dump(value, handle, ensure_ascii=False, sort_keys=True, indent=2)
                handle.write("\n")
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(name, self.state_file)
        finally:
            Path(name).unlink(missing_ok=True)

    def _discovered(self, include_root=False) -> list[dict[str, str]]:
        result = []
        candidates = []
        for configured_root in self.roots:
            candidates.append(configured_root)
            try:
                candidates.extend(child for child in configured_root.iterdir() if child.is_dir())
            except OSError:
                pass
        for root in candidates:
            local_state = root / "Local State"
            if not local_state.is_file():
                continue
            try:
                raw = json.loads(local_state.read_text(encoding="utf-8"))
                cache = raw.get("profile", {}).get("info_cache", {})
            except (OSError, ValueError, AttributeError):
                continue
            if not isinstance(cache, dict):
                continue
            for directory, info in sorted(cache.items()):
                if not isinstance(info, dict):
                    continue
                profile_id = "default" if directory == "Default" else directory
                active = os.path.lexists(root / "SingletonLock")
                record = {
                    "profile_id": profile_id,
                    "name": str(info.get("name") or profile_id),
                    "mode": "unmanaged_unavailable",
                    "reason": "ordinary_profile_active" if active else "ordinary_profile",
                }
                if include_root: record['_root'] = str(root.resolve())
                result.append(record)
        return result

    def _profile_helper(self, profile_id):
        # Resolve aliases against every discovered profile, not just paired ones.
        profiles = self._discovered(include_root=True)
        matches = [p for p in profiles if p['profile_id'] == profile_id]
        if len(matches) != 1:
            raise ProfileManagerError('profile identifier missing or ambiguous')
        selected = matches[0]
        root = selected['_root']
        same_root = [p for p in profiles if p['_root'] == root]
        if sum(p['name'] == selected['name'] for p in same_root) != 1:
            raise ProfileManagerError('profile name ambiguous')
        config = Path(os.environ.get('XDG_CONFIG_HOME', str(Path.home() / '.config'))) / 'gptadmin-extension-bridge'
        def private_json(filename):
            fd = os.open(config / filename, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, encoding='utf-8') as stream:
                st = os.fstat(stream.fileno())
                if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
                    raise ValueError('unsafe registry')
                result = json.load(stream)
                if not isinstance(result, dict): raise ValueError('invalid registry')
                return result
        try:
            bindings, roots = private_json('bindings.json'), private_json('roots.json')
            if roots.get(profile_id) != root: raise ValueError('root mismatch')
            origin = bindings[profile_id]
            if not isinstance(origin, str) or not re.fullmatch(r'chrome-extension://[a-p]{32}/', origin): raise ValueError('invalid origin')
            if sum(value == origin for value in bindings.values()) != 1: raise ValueError('duplicate origin')
            pairs = {}
            for p in same_root:
                alias = p['profile_id']
                if roots.get(alias) != root or alias not in bindings: continue
                value = bindings[alias]
                if not isinstance(value, str) or not re.fullmatch(r'chrome-extension://[a-p]{32}/', value): raise ValueError('invalid origin')
                pairs[alias] = (p['name'], value[19:-1])
            # Do not use a previously cached connection after registry changes.
            cached = self.helpers.get(root)
            if cached and cached[0] != pairs:
                if all(pairs.get(key) == value for key, value in cached[0].items()):
                    if len({value[0] for value in pairs.values()}) != len(pairs) or len({value[1] for value in pairs.values()}) != len(pairs):
                        raise ValueError('ambiguous profiles')
                    cached[1]._profiles = dict(pairs)
                    self.helpers[root] = cached = (pairs, cached[1])
                else:
                    cached[1].close()
                    del self.helpers[root]
                    cached = None
            if cached is None:
                port_file = Path(root) / 'DevToolsActivePort'
                fd = os.open(port_file, os.O_RDONLY | os.O_NOFOLLOW)
                with os.fdopen(fd, encoding='ascii') as stream:
                    st = os.fstat(stream.fileno())
                    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o022: raise ValueError('unsafe debug port file')
                    lines = stream.read(512).splitlines()
                if len(lines) != 2 or not lines[0].isdigit() or not 1 <= int(lines[0]) <= 65535 or not re.fullmatch(r'/devtools/browser/[A-Za-z0-9-]+', lines[1]): raise ValueError('invalid debug port file')
                from chrome_profile_helper import ChromeProfileHelper
                helper = ChromeProfileHelper('ws://127.0.0.1:' + lines[0] + lines[1], pairs)
                self.helpers[root] = (pairs, helper)
            else: helper = cached[1]
            return helper
        except (OSError, ValueError, KeyError, ImportError) as exc:
            raise ProfileManagerError('profile helper configuration or dependency unavailable') from exc
        except RuntimeError as exc:
            raise ProfileManagerError(str(exc)) from None

    def helper_reload(self, profile_id, extension_id, expected_current_version, expected_version):
        try:
            result = self._profile_helper(profile_id).reload(profile_id, extension_id, expected_version, expected_current_version=expected_current_version)
        except RuntimeError as exc:
            raise ProfileManagerError(str(exc)) from None
        self.helper_verified = True
        return {'profile_id': profile_id, 'mode': 'helper', 'extension': result, 'readback': 'verified'}

    def helper_stage(self, profile_id, source_path, extension_id, version, digest):
        # Verify the requested profile before returning its staged artifact.
        # Immutable stages are shared; this call does not reserve them to a profile.
        try:
            self._profile_helper(profile_id)._bind(profile_id)
        except RuntimeError as exc:
            raise ProfileManagerError(str(exc)) from None
        return {'profile_id': profile_id, 'mode': 'helper', **self.stage(source_path, extension_id, version, digest)}

    def _helper_owner(self, profile_id):
        profiles = [p for p in self._discovered(include_root=True) if p['profile_id'] == profile_id]
        if len(profiles) != 1: raise ProfileManagerError('profile identifier missing or ambiguous')
        return hashlib.sha256((profiles[0]['_root'] + '\0' + profile_id).encode()).hexdigest()

    @contextmanager
    def _helper_lock(self, profile_id):
        require_lifecycle_platform()
        owner = self._helper_owner(profile_id)
        directory = self.state_dir / 'helper-backups' / owner
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(directory / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'r+') as lock:
            st = os.fstat(lock.fileno())
            if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
                raise ProfileManagerError('unsafe helper lock')
            fcntl.flock(lock, fcntl.LOCK_EX)
            try: yield owner, directory
            finally: fcntl.flock(lock, fcntl.LOCK_UN)

    def helper_prepare(self, profile_id, stage_id):
        """Prepare a profile-owned path for a subsequent explicit installation."""
        with self._helper_lock(profile_id) as (owner, _):
            helper = self._profile_helper(profile_id)
            entries = helper.list_extensions(profile_id)
            target = self._stage(stage_id)
            if any(item.get('id') == target['extension_id'] for item in entries):
                raise ProfileManagerError('extension already installed')
            parent = self.state_dir / 'helper-extensions' / owner / target['extension_id']
            parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            active = parent / 'extension'
            if active.exists() or active.is_symlink(): raise ProfileManagerError('managed extension path already exists')
            temporary = parent / ('candidate-' + uuid.uuid4().hex)
            try:
                shutil.copytree(self.stage_dir / stage_id / 'content', temporary)
                if content_hash(temporary) != target['digest']: raise ProfileManagerError('candidate integrity failure')
                temporary.chmod(0o700)
                for item in temporary.rglob('*'):
                    if item.is_dir(): item.chmod(0o700)
                os.replace(temporary, active)
                self._sync_directory(parent)
            finally:
                if temporary.exists(): shutil.rmtree(temporary)
            return {'profile_id': profile_id, 'mode': 'helper', 'extension_id': target['extension_id'], 'version': target['version'], 'path': str(active), 'installed': False}

    def helper_update(self, profile_id, stage_id, expected_version):
        target = self._stage(stage_id)
        extension_id = target['extension_id']
        if target['version'] == expected_version: raise ProfileManagerError('update requires a distinct version')
        # The snapshot is independent and is verified again while holding the mutation lock.
        owner = self._helper_owner(profile_id)
        active = self.state_dir / 'helper-extensions' / owner / extension_id / 'extension'
        helper = self._profile_helper(profile_id)
        info = helper.unpacked_details(profile_id, extension_id)
        if active.is_symlink() or not active.is_dir() or Path(info['path']).resolve() != active.resolve():
            raise ProfileManagerError('extension is not installed from its profile-owned managed directory')
        saved = self.helper_backup(profile_id, extension_id, expected_version)
        with self._helper_lock(profile_id) as (locked_owner, backups):
            if locked_owner != owner: raise ProfileManagerError('profile changed during update')
            journal = backups / (extension_id + '.pending.json')
            if os.path.lexists(journal): raise ProfileManagerError('pending helper operation requires reconciliation')
            helper = self._profile_helper(profile_id)
            info = helper.unpacked_details(profile_id, extension_id)
            before = bridge_call(profile_id, 'get', extension_id)
            if active.is_symlink() or Path(info['path']).resolve() != active.resolve() or info['version'] != expected_version or before.get('version') != expected_version or before.get('enabled') is not True or content_hash(active) != saved['digest']:
                raise ProfileManagerError('extension changed during update precondition')
            if self._stage(stage_id) != target: raise ProfileManagerError('stage integrity failure')
            parent = active.parent
            candidate = parent / ('candidate-' + uuid.uuid4().hex)
            previous = parent / ('previous-' + uuid.uuid4().hex)
            try:
                shutil.copytree(self.stage_dir / stage_id / 'content', candidate)
                if content_hash(candidate) != target['digest']: raise ProfileManagerError('candidate integrity failure')
                candidate.chmod(0o700)
                for item in candidate.rglob('*'):
                    if item.is_dir(): item.chmod(0o700)
                record = {'extension_id': extension_id, 'before': saved, 'target_stage_id': stage_id, 'phase': 'filesystem_unknown', 'previous': previous.name}
                self._atomic_json(journal, record)
                os.replace(active, previous)
                os.replace(candidate, active)
                self._sync_directory(parent)
                record['phase'] = 'reload_unknown'
                self._atomic_json(journal, record)
                helper.reload(profile_id, extension_id, target['version'], expected_current_version=expected_version)
                if content_hash(active) != target['digest']: raise ProfileManagerError('updated artifact integrity failure')
                previous.chmod(0o700)
                for item in previous.rglob('*'):
                    if item.is_dir(): item.chmod(0o700)
                shutil.rmtree(previous); self._sync_directory(parent)
                journal.unlink(); self._sync_directory(backups)
                return {'profile_id': profile_id, 'mode': 'helper', 'extension_id': extension_id, 'version': target['version'], 'digest': target['digest'], 'backup_id': saved['backup_id'], 'readback': 'verified'}
            except RuntimeError as exc:
                raise ProfileManagerError(str(exc)) from None
            except OSError as exc:
                raise ProfileManagerError('helper update failed (OS error ' + str(exc.errno) + '); inspect pending operation before retry') from exc
            finally:
                if candidate.exists(): shutil.rmtree(candidate)

    def helper_reconcile(self, profile_id, extension_id):
        if not re.fullmatch(r'[a-p]{32}', extension_id or ''): raise ProfileManagerError('invalid extension id')
        with self._helper_lock(profile_id) as (owner, backups):
            journal = backups / (extension_id + '.pending.json')
            try:
                record = json.loads(journal.read_text(encoding='utf-8'))
                before = record['before']
                if before.get('profile_id') != profile_id or before.get('mode') != 'helper': raise ValueError()
                backup_id = before['backup_id']
                if not re.fullmatch(r'[0-9a-f]{32}', backup_id): raise ValueError()
                backup = json.loads((backups / (backup_id + '.json')).read_text(encoding='utf-8'))
                old = self._stage(backup['stage_id'])
                target = self._stage(record['target_stage_id'])
                previous_name = record['previous']
                if (record['extension_id'] != extension_id or old['extension_id'] != extension_id
                    or target['extension_id'] != extension_id or backup != {**old, 'stage_id': backup['stage_id']}
                    or any(before.get(k) != v for k, v in backup.items())
                    or old['version'] == target['version']
                    or not re.fullmatch(r'previous-[0-9a-f]{32}', previous_name)
                    or record['phase'] not in ('filesystem_unknown', 'reload_unknown')): raise ValueError()
                active = self.state_dir / 'helper-extensions' / owner / extension_id / 'extension'
                previous = active.parent / previous_name
                def digest(path):
                    if not os.path.lexists(path): return None
                    if path.is_symlink() or not path.is_dir(): raise ValueError()
                    return content_hash(path)
                active_hash, previous_hash = digest(active), digest(previous)
                helper = self._profile_helper(profile_id)
                helper._bind(profile_id)
                loaded = bridge_call(profile_id, 'get', extension_id)
                if loaded.get('id') != extension_id or loaded.get('enabled') is not True: raise ValueError()
                if active_hash is not None:
                    info = helper.unpacked_details(profile_id, extension_id)
                    if (info.get('id') != extension_id or Path(info['path']).resolve() != active.resolve()
                        or loaded.get('version') != info.get('version')): raise ValueError()
                if (record['phase'] == 'filesystem_unknown' and loaded.get('version') == old['version']
                    and active_hash in (None, old['digest'], target['digest'])
                    and previous_hash in (None, old['digest'])
                    and (active_hash == old['digest'] or previous_hash == old['digest'])):
                    if active_hash != old['digest']:
                        if active_hash is not None:
                            displaced = active.parent / ('candidate-' + uuid.uuid4().hex)
                            os.replace(active, displaced)
                        else: displaced = None
                        os.replace(previous, active)
                        self._sync_directory(active.parent)
                        if displaced is not None: shutil.rmtree(displaced)
                    outcome = 'restored'
                elif (record['phase'] == 'reload_unknown' and loaded.get('version') == target['version']
                      and active_hash == target['digest'] and previous_hash in (None, old['digest'])):
                    outcome = 'completed'
                else: raise ValueError()
                if previous.exists(): shutil.rmtree(previous)
                self._sync_directory(active.parent)
                journal.unlink(); self._sync_directory(backups)
                return {'profile_id': profile_id, 'extension_id': extension_id, 'outcome': outcome, 'version': loaded['version'], 'backup_id': backup_id}
            except (OSError, ValueError, KeyError, TypeError, RuntimeError) as exc:
                raise ProfileManagerError('helper reconciliation unavailable or ambiguous; pending operation retained') from exc

    def helper_rollback(self, profile_id, extension_id, backup_id, expected_version):
        if not re.fullmatch(r'[a-p]{32}', extension_id or ''): raise ProfileManagerError('invalid extension id')
        if not re.fullmatch(r'[0-9a-f]{32}', backup_id): raise ProfileManagerError('invalid backup id')
        owner = self._helper_owner(profile_id)
        path = self.state_dir / 'helper-backups' / owner / (backup_id + '.json')
        try:
            record = json.loads(path.read_text(encoding='utf-8'))
            target = self._stage(record['stage_id'])
            if record != {**target, 'stage_id': record['stage_id']}: raise ValueError()
        except (OSError, ValueError, KeyError, TypeError) as exc:
            raise ProfileManagerError('backup unavailable or invalid') from exc
        if record['extension_id'] != extension_id: raise ProfileManagerError('backup extension id mismatch')
        return self.helper_update(profile_id, record['stage_id'], expected_version)

    def helper_backup(self, profile_id, extension_id, expected_version):
        require_lifecycle_platform()
        profiles = [p for p in self._discovered(include_root=True) if p['profile_id'] == profile_id]
        if len(profiles) != 1: raise ProfileManagerError('profile identifier missing or ambiguous')
        root = profiles[0]['_root']
        owner = hashlib.sha256((root + '\0' + profile_id).encode()).hexdigest()
        backups = self.state_dir / 'helper-backups' / owner
        backups.mkdir(parents=True, exist_ok=True, mode=0o700)
        lock_fd = os.open(backups / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        with os.fdopen(lock_fd, 'r+') as lock:
            st = os.fstat(lock.fileno())
            if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077: raise ProfileManagerError('unsafe helper lock')
            fcntl.flock(lock, fcntl.LOCK_EX)
            try:
                if not re.fullmatch(r'[a-p]{32}', extension_id or ''): raise ProfileManagerError('invalid extension id')
                if os.path.lexists(backups / (extension_id + '.pending.json')): raise ProfileManagerError('pending helper operation requires reconciliation')
                helper = self._profile_helper(profile_id)
                info = helper.unpacked_details(profile_id, extension_id)
                before = bridge_call(profile_id, 'get', extension_id)
                if info['version'] != expected_version or before.get('version') != expected_version or before.get('enabled') is not True:
                    raise ProfileManagerError('extension version or enabled-state precondition failed')
                source = Path(info['path'])
                if not source.is_absolute() or source.is_symlink(): raise ProfileManagerError('unsafe extension directory')
                source = source.resolve(strict=True)
                if source == Path(source.anchor) or source == Path.home().resolve() or not source.is_dir() or source.stat().st_uid != os.getuid():
                    raise ProfileManagerError('unsafe extension directory')
                for profile in self._discovered(include_root=True):
                    browser_root = Path(profile['_root']).resolve()
                    if source == browser_root or source in browser_root.parents or browser_root in source.parents:
                        raise ProfileManagerError('extension directory overlaps browser profile data')
                with tempfile.TemporaryDirectory(prefix='.snapshot-', dir=self.artifact_dir) as temporary:
                    temporary = Path(temporary)
                    copy_artifact(source.parent, source.name, temporary / 'first')
                    copy_artifact(source.parent, source.name, temporary / 'second')
                    digest = content_hash(temporary / 'first')
                    if content_hash(temporary / 'second') != digest: raise ProfileManagerError('extension changed during backup')
                    manifest = json.loads((temporary / 'first' / 'manifest.json').read_text(encoding='utf-8'))
                    identity, version = extension_identity(manifest)
                    if identity != extension_id or version != expected_version: raise ProfileManagerError('extension identity or version mismatch')
                    after_info = helper.unpacked_details(profile_id, extension_id)
                    after = bridge_call(profile_id, 'get', extension_id)
                    current_profiles = [p for p in self._discovered(include_root=True) if p['profile_id'] == profile_id]
                    if current_profiles != profiles or info != after_info or after.get('version') != expected_version or after.get('enabled') is not True:
                        raise ProfileManagerError('extension changed during backup')
                    staged = self.stage(str((temporary / 'first').relative_to(self.artifact_dir)), extension_id, expected_version, digest)
                backup_id = uuid.uuid4().hex
                record = {key: staged[key] for key in ('stage_id', 'extension_id', 'version', 'digest', 'name')}
                self._atomic_json(backups / (backup_id + '.json'), record)
                return {'profile_id': profile_id, 'mode': 'helper', 'backup_id': backup_id, **record, 'readback': 'verified'}
            except (OSError, ValueError, KeyError, TypeError) as exc:
                raise ProfileManagerError('extension backup unavailable or unsafe') from exc
            except RuntimeError as exc:
                raise ProfileManagerError(str(exc)) from None
            finally:
                fcntl.flock(lock, fcntl.LOCK_UN)

    def paired_call(self, profile_id, method, extension_id=None):
        try:
            self._profile_helper(profile_id)._bind(profile_id)
        except RuntimeError as exc:
            raise ProfileManagerError(str(exc)) from None
        return bridge_call(profile_id, method, extension_id)

    def profile_list(self) -> list[dict[str, Any]]:
        controlled = []
        profiles = self.state_dir / "profiles"
        for path in sorted(profiles.iterdir()) if profiles.exists() else []:
            if not path.is_dir(): continue
            state = self._profile_state(path.name)
            items = [{**entry["current"], "pending": bool(entry.get("pending"))}
                     for entry in state["extensions"].values() if entry.get("current")]
            if items:
                record = {"profile_id": path.name, "name": path.name, "mode": "controlled", "extensions": items}
                if len(items) == 1: record["version"] = items[0]["version"]
                controlled.append(record)
        return self._discovered() + controlled

    def profile_stage(self, profile_id: str, source_path: str, extension_id: str, version: str, digest: str) -> dict[str, Any]:
        _safe_id(profile_id)
        if any(item["profile_id"] == profile_id for item in self._discovered()):
            raise ProfileManagerError("unmanaged_unavailable profile cannot be staged")
        return self.stage(source_path, extension_id, version, digest)

    def stage(self, source_path: str, extension_id: str, version: str, digest: str) -> dict[str, Any]:
        require_lifecycle_platform()
        expected = digest.removeprefix("sha256:")
        if not re.fullmatch(r"[0-9a-f]{64}", expected):
            raise ProfileManagerError("valid sha256 digest required")
        self.stage_dir.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=self.stage_dir, prefix=".stage-") as temporary:
            content = Path(temporary) / "content"
            copy_artifact(self.artifact_dir, source_path, content)
            actual = content_hash(content)
            try:
                manifest = json.loads((content / "manifest.json").read_text(encoding="utf-8"))
                actual_id, actual_version = extension_identity(manifest)
            except (OSError, ValueError, TypeError, KeyError) as exc:
                raise ProfileManagerError("extension manifest invalid") from exc
            if (actual, actual_id, actual_version) != (expected, extension_id, version):
                raise ProfileManagerError("extension hash, identity or version mismatch")
            stage_id = hashlib.sha256((actual_id + ":" + actual_version + ":" + actual).encode()).hexdigest()
            destination = self.stage_dir / stage_id
            metadata = {"extension_id": actual_id, "version": actual_version, "digest": actual, "name": manifest["name"]}
            (Path(temporary) / "stage.json").write_text(json.dumps(metadata), encoding="utf-8")
            for item in content.rglob("*"):
                item.chmod(0o555 if item.is_dir() else 0o444)
            content.chmod(0o555)
            (Path(temporary) / "stage.json").chmod(0o444)
            if destination.exists():
                if self._stage(stage_id) != metadata:
                    raise ProfileManagerError("existing stage integrity failure")
            else:
                try:
                    os.rename(temporary, destination)
                    destination.chmod(0o555)
                except FileExistsError:
                    if self._stage(stage_id) != metadata:
                        raise ProfileManagerError("existing stage integrity failure")
            return {"stage_id": stage_id, **metadata, "immutable": True}

    def _stage(self, stage_id: str) -> dict[str, Any]:
        if not re.fullmatch(r"[0-9a-f]{64}", stage_id):
            raise ProfileManagerError("invalid stage id")
        path = self.stage_dir / stage_id
        try:
            metadata = json.loads((path / "stage.json").read_text(encoding="utf-8"))
            manifest = json.loads((path / "content" / "manifest.json").read_text(encoding="utf-8"))
            identity, version = extension_identity(manifest)
            digest = content_hash(path / "content")
            calculated = hashlib.sha256((identity + ":" + version + ":" + digest).encode()).hexdigest()
            if metadata != {"extension_id": identity, "version": version, "digest": digest, "name": manifest["name"]} or calculated != stage_id:
                raise ValueError("mismatch")
            return metadata
        except (OSError, ValueError, TypeError, KeyError) as exc:
            raise ProfileManagerError("stage integrity failure") from exc

    def install(self, profile_id, stage_id, expected_version=None):
        return self._apply(profile_id, stage_id, expected_version, "install")

    def update(self, profile_id, stage_id, expected_version):
        return self._apply(profile_id, stage_id, expected_version, "update")

    def backup(self, profile_id, expected_version, extension_id=None):
        backup_id = uuid.uuid4().hex
        with self._locked(profile_id):
            result = self._run_lifecycle(profile_id, {"operation":"backup", "expected_version":expected_version,
                                         "extension_id":extension_id, "backup_id":backup_id})
            return {"backup_id":backup_id, **result}

    def rollback(self, profile_id, backup_id, expected_version, extension_id=None):
        _safe_id(backup_id)
        with self._locked(profile_id):
            try: backup = json.loads((self._profile_root(profile_id) / "backups" / (backup_id + ".json")).read_text())
            except (OSError, ValueError) as exc: raise ProfileManagerError("backup unavailable") from exc
            target = {**self._stage(backup["stage_id"]), "stage_id":backup["stage_id"]}
            if target != backup: raise ProfileManagerError("backup integrity failure")
            result = self._run_lifecycle(profile_id, {"operation":"rollback", "target":target,
                                         "extension_id":extension_id, "expected_version":expected_version})
            return {"profile_id":profile_id, **result, "readback":"verified"}

    def reload(self, profile_id, expected_version, extension_id=None):
        with self._locked(profile_id):
            result = self._run_lifecycle(profile_id, {"operation":"reload", "expected_version":expected_version, "extension_id":extension_id})
            return {"profile_id":profile_id, **result, "readback":"verified"}

    def reconcile(self, profile_id, extension_id=None):
        with self._locked(profile_id):
            result = self._run_lifecycle(profile_id, {"operation":"reconcile", "extension_id":extension_id})
            if result.get("status") == "pre_mutation_recovered" and result.get("version") is not None and profile_id in self.backends:
                self._deferred_restore.add((profile_id, result["extension_id"]))
            if result.get("status"): return {"profile_id":profile_id, **result}
            return {"profile_id":profile_id, **result, "readback":"verified"}

    def _active(self, profile_id, entry):
        return self._profile_root(profile_id) / entry["path"]

    def _run_lifecycle(self, profile_id, request):
        # The core owns all choices and produces ordered, inspectable effect descriptions.
        plan = lifecycle_transition(self._profile_state(profile_id), request)
        context = {}
        self._execute_effects(profile_id, plan["state"], plan["effects"], context)
        return plan["result"]

    def _execute_effects(self, profile_id, state, effects, context):
        for effect in effects:
            kind = effect["kind"]
            if kind == "verify_readback":
                self._readback(profile_id, effect["item"])
            elif kind == "ensure_absent":
                if any(x["id"] == effect["extension_id"] for x in self._listing(profile_id)):
                    raise ProfileManagerError("extension already exists outside manager state")
            elif kind == "verify_active":
                if content_hash(self._profile_root(profile_id) / effect["path"]) != effect["item"]["digest"]:
                    raise ProfileManagerError("active content integrity failure")
            elif kind == "verify_stage":
                item = effect["item"]
                if {**self._stage(item["stage_id"]), "stage_id":item["stage_id"]} != item:
                    raise ProfileManagerError("stage integrity failure")
            elif kind == "write_backup":
                backups = self._profile_root(profile_id) / "backups"; backups.mkdir(exist_ok=True, mode=0o700)
                self._atomic_json(backups / (effect["backup_id"] + ".json"), effect["item"])
            elif kind == "prepare":
                active = self._profile_root(profile_id) / effect["path"]
                active.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                candidate = active.parent / ("candidate-" + uuid.uuid4().hex)
                shutil.copytree(self.stage_dir / effect["item"]["stage_id"] / "content", candidate)
                if content_hash(candidate) != effect["item"]["digest"]: raise ProfileManagerError("candidate integrity failure")
                candidate.chmod(0o700)
                for directory in candidate.rglob("*"):
                    if directory.is_dir(): directory.chmod(0o700)
                context["candidate"] = candidate
            elif kind == "persist":
                self._save_profile(profile_id, state)
            elif kind == "activate":
                active = self._profile_root(profile_id) / effect["path"]
                previous = active.parent / ("previous-" + effect["extension_id"])
                if previous.exists(): shutil.rmtree(previous)
                if active.exists():
                    os.replace(active, previous)
                    self._sync_directory(active.parent)
                os.replace(context["candidate"], active)
                self._sync_directory(active.parent)
            elif kind == "set_phase":
                phase = lifecycle_transition(state, {"operation":"set_phase", "extension_id":effect["extension_id"], "phase":effect["phase"]})
                self._execute_effects(profile_id, phase["state"], phase["effects"], context)
                state.clear(); state.update(phase["state"])
            elif kind == "restore_pre_mutation":
                self._restore_pre_mutation(profile_id, effect)
            elif kind == "mutate":
                item = effect["item"]; action = effect["action"]
                args = {"id":item["extension_id"]} if action == "reload_extension" else {"path":str(self._profile_root(profile_id) / effect["path"])}
                try:
                    answer = self._backend(profile_id).tool(action, args)
                    if action == "install_extension" and not re.search(r'Extension installed\. Id: ' + re.escape(item["extension_id"]) + r'\b', answer):
                        raise ProfileManagerError("installed extension identity mismatch")
                except ProfileManagerError as exc:
                    self._readback(profile_id, item)
                    if effect.get("reload"): raise ProfileManagerError("reload outcome unknown; readback cannot prove reload") from exc
            elif kind == "finish":
                completion = lifecycle_transition(state, {"operation":"complete", "extension_id":effect["extension_id"]})
                self._execute_effects(profile_id, completion["state"], completion["effects"], context)
            elif kind == "unprovable_reload":
                raise ProfileManagerError("reload outcome unknown; readback cannot prove reload")
            else:
                raise ProfileManagerError("unknown lifecycle effect")

    @staticmethod
    def _sync_directory(path):
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(fd)
        finally: os.close(fd)

    def _restore_pre_mutation(self, profile_id, effect):
        """Restore known content only; never call or mutate the browser."""
        active = self._profile_root(profile_id) / effect["path"]
        previous = effect["item"]; target = effect["target"]
        if active.is_symlink(): raise ProfileManagerError("active content integrity failure")
        if active.exists():
            if not active.is_dir(): raise ProfileManagerError("active content integrity failure")
            actual = content_hash(active)
            permitted = {target["digest"]}
            if previous: permitted.add(previous["digest"])
            if actual not in permitted: raise ProfileManagerError("active content integrity failure")
            if previous and actual == previous["digest"]:
                self._sync_directory(active.parent)
                return
        active.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        candidate = None
        if previous:
            if {**self._stage(previous["stage_id"]), "stage_id":previous["stage_id"]} != previous:
                raise ProfileManagerError("previous stage integrity failure")
            candidate = active.parent / ("recovery-" + uuid.uuid4().hex)
            shutil.copytree(self.stage_dir / previous["stage_id"] / "content", candidate)
            if content_hash(candidate) != previous["digest"]: raise ProfileManagerError("previous stage integrity failure")
            candidate.chmod(0o700)
            for directory in candidate.rglob("*"):
                if directory.is_dir(): directory.chmod(0o700)
        if active.exists(): os.replace(active, active.parent / ("retired-" + uuid.uuid4().hex))
        if candidate: os.replace(candidate, active)
        self._sync_directory(active.parent)

    def _profile_root(self, profile_id):
        return self.state_dir / "profiles" / _safe_id(profile_id)

    @contextmanager
    def _locked(self, profile_id):
        require_lifecycle_platform()
        if any(x["profile_id"] == profile_id for x in self._discovered()):
            raise ProfileManagerError("unmanaged_unavailable profile")
        root = self._profile_root(profile_id); root.mkdir(parents=True, exist_ok=True, mode=0o700)
        with (root / ".lock").open("a") as lock:
            try: fcntl.flock(lock, fcntl.LOCK_EX)
            except OSError as exc: raise ProfileManagerError("interprocess profile lock unavailable") from exc
            try: yield
            finally: fcntl.flock(lock, fcntl.LOCK_UN)

    def _profile_state(self, profile_id):
        path = self._profile_root(profile_id) / "state.json"
        if not path.exists(): return {"extensions": {}}
        try: state = json.loads(path.read_text())
        except (OSError, ValueError) as exc: raise ProfileManagerError("profile state invalid") from exc
        if "extensions" in state:
            if not isinstance(state["extensions"], dict): raise ProfileManagerError("profile state invalid")
            return state
        current = state.get("current"); pending = state.get("pending")
        item = current or pending
        if not item: return {"extensions": {}}
        return {"extensions": {item["extension_id"]: {"current": current, "pending": pending, "path": "extension"}}}

    def _atomic_json(self, path, value):
        fd, name = tempfile.mkstemp(dir=path.parent, prefix=".write-")
        try:
            with os.fdopen(fd, "w") as handle:
                json.dump(value, handle); handle.flush(); os.fsync(handle.fileno())
            os.replace(name, path)
            fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try: os.fsync(fd)
            finally: os.close(fd)
        finally: Path(name).unlink(missing_ok=True)

    def _save_profile(self, profile_id, value):
        self._atomic_json(self._profile_root(profile_id) / "state.json", value)

    def _backend(self, profile_id):
        if profile_id not in self.backends:
            lock = (self._profile_root(profile_id) / ".backend.lock").open("a")
            try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                lock.close(); raise ProfileManagerError("profile backend owned by another manager") from exc
            except OSError as exc:
                lock.close(); raise ProfileManagerError("interprocess backend lock unavailable") from exc
            try: backend = self.backend_factory(self._profile_root(profile_id))
            except Exception:
                lock.close(); raise
            self.backends[profile_id] = backend; self.backend_locks[profile_id] = lock
            state = self._profile_state(profile_id)
            for extension_id in state["extensions"]:
                observed = self._listing(profile_id)
                plan = lifecycle_transition(self._profile_state(profile_id), {"operation":"restore", "extension_id":extension_id, "observed":observed})
                self._execute_effects(profile_id, plan["state"], plan["effects"], {})
        if not self._restoring:
            self._restoring = True
            try:
                for owner, extension_id in sorted(self._deferred_restore):
                    if owner != profile_id: continue
                    observed = self._listing(profile_id)
                    plan = lifecycle_transition(self._profile_state(profile_id), {"operation":"restore", "extension_id":extension_id, "observed":observed})
                    self._execute_effects(profile_id, plan["state"], plan["effects"], {})
                    self._deferred_restore.discard((owner, extension_id))
            finally:
                self._restoring = False
        return self.backends[profile_id]

    def _listing(self, profile_id):
        response = self._backend(profile_id).call("tools/call", {"name": "list_extensions", "arguments": {}})
        entries = response.get("structuredContent", {}).get("extensions")
        if not isinstance(entries, list): raise ProfileManagerError("structured extension count unavailable")
        if all(isinstance(x, dict) and all(k in x for k in ("id", "version", "enabled")) for x in entries):
            result = entries
        else:
            content = response.get("content")
            if not isinstance(content, list) or len(content) != 1 or content[0].get("type") != "text": raise ProfileManagerError("extension listing invalid")
            text = content[0].get("text", "")
            if not entries and text == "## Extensions\nNo extensions installed.":
                self._mark_runtime_verified(profile_id)
                return []
            lines = text.split("\n")
            if not lines or lines[0] != "## Extensions" or len(lines) - 1 != len(entries): raise ProfileManagerError("extension listing ambiguous")
            result = []
            for line in lines[1:]:
                match = re.fullmatch(r'id=([a-p]{32}) "([^\r\n]*)" v([0-9]+(?:\.[0-9]+){0,3}) (Enabled|Disabled)', line)
                if not match: raise ProfileManagerError("extension listing ambiguous")
                result.append({"id": match[1], "name": match[2], "version": match[3], "enabled": match[4] == "Enabled"})
        ids = [x.get("id") for x in result]
        if len(ids) != len(set(ids)) or any(not isinstance(x, str) or not re.fullmatch(r"[a-p]{32}", x) for x in ids):
            raise ProfileManagerError("extension listing ambiguous")
        self._mark_runtime_verified(profile_id)
        return result

    def _mark_runtime_verified(self, profile_id):
        backend = self.backends.get(profile_id)
        if isinstance(backend, Backend): backend.runtime_verified = True

    @staticmethod
    def _check_record(matches, item):
        if len(matches) != 1 or matches[0].get("version") != item["version"] or matches[0].get("enabled") is not True:
            raise ProfileManagerError("extension readback mismatch")

    def _readback(self, profile_id, item):
        self._check_record([x for x in self._listing(profile_id) if x["id"] == item["extension_id"]], item)

    def _apply(self, profile_id, stage_id, expected, operation):
        with self._locked(profile_id):
            target = {**self._stage(stage_id), "stage_id":stage_id}
            result = self._run_lifecycle(profile_id, {"operation":operation, "target":target, "expected_version":expected})
            return {"profile_id":profile_id, **result, "readback":"verified"}

    def close(self):
        for _, helper in self.helpers.values(): helper.close()
        self.helpers.clear()
        self.helper_verified = False
        for backend in self.backends.values(): backend.close()
        for lock in self.backend_locks.values(): lock.close()
        self.backends.clear(); self.backend_locks.clear()
        self._deferred_restore.clear()


TOOLS = [
    {"name": "profile_helper_prepare", "description": "Prepare a profile-owned directory from an immutable stage; does not install it", "inputSchema": {"type": "object", "required": ["profile_id", "stage_id"], "properties": {"profile_id": {"type": "string"}, "stage_id": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_helper_update", "description": "Update an extension installed from its profile-owned directory, backing up and verifying loaded version", "inputSchema": {"type": "object", "required": ["profile_id", "stage_id", "expected_version"], "properties": {"profile_id": {"type": "string"}, "stage_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_helper_reconcile", "description": "Reconcile a pending helper operation without repeating reload", "inputSchema": {"type": "object", "required": ["profile_id", "extension_id"], "properties": {"profile_id": {"type": "string"}, "extension_id": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_helper_rollback", "description": "Restore an artifact backup of a profile-owned extension and verify the loaded version", "inputSchema": {"type": "object", "required": ["profile_id", "extension_id", "backup_id", "expected_version"], "properties": {"profile_id": {"type": "string"}, "extension_id": {"type": "string"}, "backup_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_helper_backup", "description": "Back up a verified unpacked extension artifact from a paired profile, excluding profile and extension storage", "inputSchema": {"type": "object", "required": ["profile_id", "extension_id", "expected_version"], "properties": {"profile_id": {"type": "string"}, "extension_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_helper_stage", "description": "Validate and stage an immutable artifact for a verified paired existing profile", "inputSchema": {"type": "object", "required": ["profile_id", "source_path", "extension_id", "version", "digest"], "properties": {"profile_id": {"type": "string"}, "source_path": {"type": "string"}, "extension_id": {"type": "string"}, "version": {"type": "string"}, "digest": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_helper_reload", "description": "Reload in a paired existing profile using its explicitly authorized Chrome debugging channel; verify loaded version", "inputSchema": {"type": "object", "required": ["profile_id", "extension_id", "expected_current_version", "expected_version"], "properties": {"profile_id": {"type": "string"}, "extension_id": {"type": "string"}, "expected_current_version": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_extensions_list", "description": "List extensions in a paired existing profile", "inputSchema": {"type": "object", "required": ["profile_id"], "properties": {"profile_id": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_extension_get", "description": "Read an extension in a paired existing profile", "inputSchema": {"type": "object", "required": ["profile_id", "extension_id"], "properties": {"profile_id": {"type": "string"}, "extension_id": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_extension_set_enabled", "description": "Enable or disable an extension in a paired existing profile and return Chrome readback", "inputSchema": {"type": "object", "required": ["profile_id", "extension_id", "enabled"], "properties": {"profile_id": {"type": "string"}, "extension_id": {"type": "string"}, "enabled": {"type": "boolean"}}, "additionalProperties": False}},
    {"name": "profile_list", "description": "List profile names and report controlled, helper, policy and unmanaged availability; backend configuration is not runtime proof", "inputSchema": {"type": "object", "additionalProperties": False}},
    {"name": "profile_stage", "description": "Stage content from a relative path under the manager artifacts directory; verify hash, key-derived identity and version", "inputSchema": {"type": "object", "required": ["profile_id", "source_path", "extension_id", "version", "digest"], "properties": {"profile_id": {"type": "string"}, "source_path": {"type": "string"}, "extension_id": {"type": "string"}, "version": {"type": "string"}, "digest": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_install", "description": "Install a staged controlled profile with CAS", "inputSchema": {"type": "object", "required": ["profile_id", "stage_id"], "properties": {"profile_id": {"type": "string"}, "stage_id": {"type": "string"}, "expected_version": {"type": ["string", "null"]}}, "additionalProperties": False}},
    {"name": "profile_update", "description": "Update a controlled profile with CAS", "inputSchema": {"type": "object", "required": ["profile_id", "stage_id", "expected_version"], "properties": {"profile_id": {"type": "string"}, "stage_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_reload", "description": "Reload through a controlled pipe backend and verify loaded version", "inputSchema": {"type": "object", "required": ["profile_id", "expected_version"], "properties": {"extension_id": {"type": "string"}, "profile_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_backup", "description": "Back up verified extension artifact with CAS; excludes profile and extension storage", "inputSchema": {"type": "object", "required": ["profile_id", "expected_version"], "properties": {"extension_id": {"type": "string"}, "profile_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_rollback", "description": "Restore backed-up extension artifact with CAS and Chrome readback", "inputSchema": {"type": "object", "required": ["profile_id", "backup_id", "expected_version"], "properties": {"extension_id": {"type": "string"}, "profile_id": {"type": "string"}, "backup_id": {"type": "string"}, "expected_version": {"type": "string"}}, "additionalProperties": False}},
    {"name": "profile_reconcile", "description": "Read back a pending operation without repeating it", "inputSchema": {"type": "object", "required": ["profile_id"], "properties": {"extension_id": {"type": "string"}, "profile_id": {"type": "string"}}, "additionalProperties": False}},
]


def _reply(message_id: Any, result: dict[str, Any] | None = None, error: dict[str, Any] | None = None) -> None:
    payload: dict[str, Any] = {"jsonrpc": "2.0", "id": message_id}
    payload["result" if error is None else "error"] = result if error is None else error
    print(json.dumps(payload, ensure_ascii=False), flush=True)


def main() -> None:
    manager = ProfileManager()
    for line in sys.stdin:
        request: dict[str, Any] = {}
        try:
            request = json.loads(line)
            method = request.get("method")
            if method == "initialize":
                requested = (request.get("params") or {}).get("protocolVersion")
                negotiated = requested if requested in ("2025-06-18", "2025-11-25") else "2025-06-18"
                _reply(request.get("id"), {"protocolVersion": negotiated, "capabilities": {"tools": {}}, "serverInfo": {"name": "chrome-profile-manager", "version": "1.0.0"}})
            elif method == "tools/list":
                _reply(request.get("id"), {"tools": TOOLS})
            elif method == "tools/call":
                params = request.get("params") or {}; name = params.get("name"); args = params.get("arguments") or {}
                if name == "profile_helper_prepare":
                    value = manager.helper_prepare(str(args['profile_id']), str(args['stage_id']))
                elif name == "profile_helper_update":
                    value = manager.helper_update(str(args['profile_id']), str(args['stage_id']), str(args['expected_version']))
                elif name == "profile_helper_reconcile":
                    value = manager.helper_reconcile(str(args['profile_id']), str(args['extension_id']))
                elif name == "profile_helper_rollback":
                    value = manager.helper_rollback(str(args['profile_id']), str(args['extension_id']), str(args['backup_id']), str(args['expected_version']))
                elif name == "profile_helper_backup":
                    value = manager.helper_backup(str(args['profile_id']), str(args['extension_id']), str(args['expected_version']))
                elif name == "profile_helper_stage":
                    value = manager.helper_stage(str(args['profile_id']), str(args['source_path']), str(args['extension_id']), str(args['version']), str(args['digest']))
                elif name == "profile_helper_reload":
                    value = manager.helper_reload(str(args['profile_id']), str(args['extension_id']), str(args['expected_current_version']), str(args['expected_version']))
                elif name == "profile_extensions_list":
                    value = {"extensions": manager.paired_call(str(args["profile_id"]), "list")}
                elif name == "profile_extension_get":
                    value = {"extension": manager.paired_call(str(args["profile_id"]), "get", str(args["extension_id"]))}
                elif name == "profile_extension_set_enabled":
                    if not isinstance(args["enabled"], bool): raise ProfileManagerError("enabled must be boolean")
                    value = {"extension": manager.paired_call(str(args["profile_id"]), "enable" if args["enabled"] else "disable", str(args["extension_id"]))}
                elif name == "profile_list":
                    value = {"profiles": manager.profile_list(), "capabilities": manager.capabilities()}
                elif name == "profile_stage":
                    value = manager.profile_stage(str(args["profile_id"]), str(args["source_path"]), str(args["extension_id"]), str(args["version"]), str(args["digest"]))
                elif name == "profile_install":
                    value = manager.install(str(args["profile_id"]), str(args["stage_id"]), args.get("expected_version"))
                elif name == "profile_update":
                    value = manager.update(str(args["profile_id"]), str(args["stage_id"]), str(args["expected_version"]))
                elif name == "profile_reload":
                    value = manager.reload(str(args["profile_id"]), str(args["expected_version"]), args.get("extension_id"))
                elif name == "profile_backup":
                    value = manager.backup(str(args["profile_id"]), str(args["expected_version"]), args.get("extension_id"))
                elif name == "profile_rollback":
                    value = manager.rollback(str(args["profile_id"]), str(args["backup_id"]), str(args["expected_version"]), args.get("extension_id"))
                elif name == "profile_reconcile":
                    value = manager.reconcile(str(args["profile_id"]), args.get("extension_id"))
                else:
                    raise ProfileManagerError("unknown tool")
                _reply(request.get("id"), {"content": [{"type": "text", "text": json.dumps(value, ensure_ascii=False)}], "structuredContent": value, "isError": False})
            elif "id" in request:
                _reply(request.get("id"), error={"code": -32601, "message": "method not found"})
        except ProfileManagerError as exc:
            _reply(request.get("id") if isinstance(request, dict) else None, error={"code": -32000, "message": str(exc)})
        except (ValueError, TypeError, KeyError) as exc:
            _reply(request.get("id") if isinstance(request, dict) else None, error={"code": -32600, "message": f"invalid request: {exc}"})
    manager.close()


if __name__ == "__main__":
    main()
