#!/usr/bin/env python3
"""GPTAdmin self-hosted hub failover watchdog.

Runs on a fallback node. It intentionally does not require an external
coordinator. Fencing is deterministic: each fallback node has a configured rank;
rank 1 promotes after fail_count_base failures, rank 2 after 2*base, etc.
Before promotion it performs a second short check against the public hub URL.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import subprocess
import sys
import time
from datetime import datetime
try:
    from gptadmin_frpc_processes import config_paths, read_records, write_records, inspect, stop_records, ProcessArgumentsPending
except ModuleNotFoundError:
    from scripts.gptadmin_frpc_processes import config_paths, read_records, write_records, inspect, stop_records, ProcessArgumentsPending
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

DEFAULT_CONFIG = "/etc/gptadmin/failover_config.json"
DEFAULT_STATE = "/etc/gptadmin/failover_state.json"
DEFAULT_RUNTIME = "/var/lib/gptadmin/failover_watchdog_state.json"
DEFAULT_FRPC = "/etc/gptadmin/frpc-failover.toml"
DEFAULT_FRPC_PID = "/var/lib/gptadmin/failover_frpc.pid"
DEFAULT_RECLAIM_COMMAND = "/var/lib/gptadmin/failover_reclaim_command.json"
DEFAULT_SERVICE = "gptadmin-failover-frpc.service"


def maintenance_deadline(value: Any) -> float | None:
    """Parse an optional finite deadline; a timezone is required."""
    if value is None or value == "":
        return None
    message = "maintenance_until must be an ISO-8601 timestamp with a timezone"
    if not isinstance(value, str):
        raise ValueError(message)
    try:
        deadline = datetime.fromisoformat(value.replace("Z", "+00:00"))
        if deadline.tzinfo is None or deadline.utcoffset() is None:
            raise ValueError(message)
        return deadline.timestamp()
    except (ValueError, OverflowError, OSError) as exc:
        raise ValueError(message) from exc


def load_json(path: str, default: Any) -> Any:
    try:
        return json.loads(Path(path).read_text())
    except FileNotFoundError:
        return default
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid JSON {path}: {exc}") from exc


def save_json(path: str, data: Any, mode: int = 0o600) -> None:
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_suffix(p.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
    os.chmod(tmp, mode)
    tmp.replace(p)


def http_ok(url: str, timeout: float) -> bool:
    if not url:
        return False
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "gptadmin-failover-watchdog/1"})
        with urllib.request.urlopen(req, timeout=timeout) as r:  # noqa: S310 - admin configured URL
            return 200 <= int(getattr(r, "status", 0)) < 500
    except (OSError, urllib.error.URLError, urllib.error.HTTPError):
        return False


def http_json(url: str, timeout: float, bearer: str = "") -> dict[str, Any] | None:
    if not url:
        return None
    try:
        headers = {"User-Agent": "gptadmin-failover-watchdog/1"}
        if bearer:
            headers["Authorization"] = "Bearer " + bearer
        req = urllib.request.Request(url, headers=headers)
        with urllib.request.urlopen(req, timeout=timeout) as r:  # noqa: S310 - admin configured URL
            if not (200 <= int(getattr(r, "status", 0)) < 300):
                return None
            return json.loads(r.read().decode())
    except (OSError, urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError):
        return None


def choose_node(cfg: dict[str, Any], node_id: str) -> dict[str, Any]:
    for node in cfg.get("nodes") or []:
        if node.get("server_id") == node_id:
            return node
    raise SystemExit(f"node_id {node_id!r} is not configured in failover nodes")


def threshold(cfg: dict[str, Any], node: dict[str, Any]) -> int:
    base = int(cfg.get("fail_count_base") or 3)
    rank = int(node.get("rank") or 1)
    if cfg.get("deterministic_rank_backoff", True):
        return max(1, base * max(1, rank))
    return max(1, base)


def state_public_url(bundle: dict[str, Any], cfg: dict[str, Any]) -> str:
    return str(
        cfg.get("primary_public_url")
        or bundle.get("hub_public_url")
        or ((bundle.get("tunnel") or {}).get("frp") or {}).get("public_url")
        or ""
    ).rstrip("/")


def frp_endpoint_specs(bundle: dict[str, Any]) -> list[tuple[str, str, int]]:
    """Return the configured FRP servers as independent client specs."""

    frp = ((bundle.get("tunnel") or {}).get("frp") or {})
    endpoints = frp.get("endpoints") or []
    if isinstance(endpoints, str):
        endpoints = [x.strip() for x in endpoints.split(",") if x.strip()]
    if not endpoints:
        addr = frp.get("server_addr") or ""
        port = frp.get("server_port") or "7000"
        if addr:
            endpoints = [f"primary={addr}:{port}"]
    specs: list[tuple[str, str, int]] = []
    for idx, endpoint in enumerate(endpoints):
        label = f"e{idx+1}"
        hostport = str(endpoint)
        if "=" in hostport:
            label, hostport = hostport.split("=", 1)
        host, _, port = hostport.partition(":")
        specs.append((label, host, int(port or str(frp.get("server_port") or "7000"))))
    return specs


def build_frpc_config(bundle: dict[str, Any], local_port: int, node_id: str, endpoint_index: int = 0) -> str:
    """Build one valid FRP v0.64 client config for one server endpoint."""

    frp = ((bundle.get("tunnel") or {}).get("frp") or {})
    token = frp.get("token") or os.environ.get("FRP_TOKEN") or ""
    subdomain = frp.get("subdomain") or ""
    domain = frp.get("domain") or ""
    endpoints = frp_endpoint_specs(bundle)
    if not token or not subdomain or not endpoints:
        raise SystemExit("failover state does not contain enough FRP data: token/subdomain/endpoints required")
    if endpoint_index < 0 or endpoint_index >= len(endpoints):
        raise IndexError(f"FRP endpoint index out of range: {endpoint_index}")
    label, host, port = endpoints[endpoint_index]
    suffix = "" if endpoint_index == 0 else f"-{label}"
    parts: list[str] = [
        "# generated by gptadmin_failover_watchdog.py",
        f"# node_id = {node_id}",
        "",
        f"serverAddr = {json.dumps(host)}",
        f"serverPort = {port}",
        "",
        "[auth]",
        f"token = {json.dumps(token)}",
    ]
    if frp.get("proxy_url"):
        parts += ["", "[transport]", f"proxyURL = {json.dumps(frp['proxy_url'])}"]
    parts += ["", "[transport.tls]", "enable = true"]
    # Match the actual FRP endpoint: TLS-sniffing proxies may otherwise
    # override the destination with the unrelated public routing domain.
    parts.append(f"serverName = {json.dumps(host)}")
    parts += [
        "",
        "[[proxies]]",
        f"name = {json.dumps('gptadmin-failover-' + subdomain + suffix)}",
        'type = "http"',
        'localIP = "127.0.0.1"',
        f"localPort = {int(local_port)}",
        f"subdomain = {json.dumps(subdomain)}",
    ]
    return "\n".join(parts) + "\n"


def run(cmd: list[str], dry_run: bool) -> None:
    if dry_run:
        print("DRY-RUN", " ".join(cmd))
        return
    subprocess.run(cmd, check=True)


def reclaim_sig_input(action: str, node_id: str, nonce: str, issued_at: int, expires_at: int, primary_health_url: str) -> str:
    return "\n".join([action, node_id, nonce, str(issued_at), str(expires_at), primary_health_url])


def reclaim_secret(bundle: dict[str, Any]) -> str:
    """Load the signing key from state or the existing runtime environment."""

    secrets = bundle.get("secrets") or {}
    return str(
        secrets.get("bridge_key")
        or secrets.get("ctl_token")
        or os.environ.get("MCP_BRIDGE_KEY")
        or os.environ.get("CTL_TOKEN")
        or ""
    )


def sign_reclaim(secret: str, text: str) -> str:
    digest = hmac.new(secret.encode(), text.encode(), hashlib.sha256).digest()
    return base64.urlsafe_b64encode(digest).decode().rstrip("=")


def load_verified_reclaim_command(args: argparse.Namespace, cfg: dict[str, Any], bundle: dict[str, Any], runtime: dict[str, Any]) -> dict[str, Any] | None:
    path = Path(args.reclaim_command_file)
    if not path.exists():
        return None
    payload = load_json(str(path), {})
    if not payload or payload.get("action") != "demote" or payload.get("node_id") != args.node_id:
        return None
    secret = reclaim_secret(bundle)
    if not secret:
        return None
    try:
        issued_at = int(payload.get("issued_at"))
        expires_at = int(payload.get("expires_at"))
    except (TypeError, ValueError):
        return None
    now = int(time.time())
    max_age = int(cfg.get("reclaim_max_age_sec") or 120)
    if issued_at > now + 30 or expires_at < now or now - issued_at > max_age:
        try:
            path.rename(path.with_suffix(path.suffix + f".expired.{now}"))
        except OSError:
            pass
        return None
    nonce = str(payload.get("nonce") or "")
    seen = list(runtime.get("reclaim_nonces") or [])[-64:]
    if not nonce or nonce in seen:
        try:
            path.unlink()
        except OSError:
            pass
        return None
    primary_health_url = str(payload.get("primary_health_url") or "")
    text = reclaim_sig_input("demote", args.node_id, nonce, issued_at, expires_at, primary_health_url)
    expected = sign_reclaim(secret, text)
    if not hmac.compare_digest(expected, str(payload.get("signature") or "")):
        return None
    runtime["reclaim_nonces"] = (seen + [nonce])[-64:]
    try:
        path.rename(path.with_suffix(path.suffix + f".handled.{nonce}"))
    except OSError:
        try:
            path.unlink()
        except OSError:
            pass
    return payload


def demote(args: argparse.Namespace, runtime: dict[str, Any], dry_run: bool) -> None:
    if args.frpc_service and args.frpc_service != "none":
        run(["systemctl", "stop", args.frpc_service], dry_run)
    else:
        paths = [Path(args.frpc_config)] + config_paths(args.frpc_config, 16)
        if dry_run:
            print("DRY-RUN stop recorded owned FRP clients")
        else:
            stop_records(args.frpc_pid_file, args.frpc_bin, paths)
    runtime["frpc_pid"] = 0
    runtime["last_demote_at"] = time.time()


def inspect_new_frpc_child(proc, binary, paths):
    # The owned, unreaped Popen child cannot have its PID reused. Linux can
    # briefly expose an incomplete /proc cmdline immediately after exec.
    # Retry only that observation, then require every normal ownership check.
    for attempt in range(20):
        try:
            return inspect(proc.pid, binary, paths)
        except ProcessArgumentsPending:
            if proc.poll() is not None or attempt == 19:
                raise
            time.sleep(0.005)


def promote(args: argparse.Namespace, cfg: dict[str, Any], bundle: dict[str, Any], node: dict[str, Any], dry_run: bool) -> None:
    local_port = int(node.get("local_hub_port") or ((bundle.get("tunnel") or {}).get("frp") or {}).get("local_port") or 9001)
    if args.hub_service and args.hub_service != "none":
        run(["systemctl", "start", args.hub_service], dry_run)
    endpoint_count = len(frp_endpoint_specs(bundle))
    config_paths = [Path(args.frpc_config)]
    if endpoint_count > 1:
        base = Path(args.frpc_config)
        config_paths = [base.with_name(f"{base.stem}-{idx}{base.suffix}") for idx in range(1, endpoint_count + 1)]
    for idx, path in enumerate(config_paths):
        text = build_frpc_config(bundle, local_port, args.node_id, idx)
        if dry_run:
            print("DRY-RUN write", path)
            print("FRP credentials omitted")
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(path.suffix + ".tmp")
        tmp.write_text(text)
        os.chmod(tmp, 0o600)
        tmp.replace(path)
    if args.frpc_service and args.frpc_service != "none":
        run(["systemctl", "restart", args.frpc_service], dry_run)
    else:
        existing = read_records(args.frpc_pid_file, args.frpc_bin, config_paths)
        if len(existing) == len(config_paths) and {r['config'] for r in existing} == {str(p.absolute()) for p in config_paths}:
            # A prolonged primary outage must not spawn another set every
            # promotion cooldown and discard the previous clients' owners.
            return
        if existing and not dry_run:
            stop_records(args.frpc_pid_file, args.frpc_bin, config_paths)
        records = []
        for path in config_paths:
            if dry_run:
                print("DRY-RUN", args.frpc_bin, "-c", path)
            else:
                # A crash/write failure after Popen must not permit another
                # launch with an unregistered child. Persist the hold first.
                Path(args.frpc_pid_file).parent.mkdir(parents=True, exist_ok=True)
                write_records(args.frpc_pid_file, records, pending=True)
                proc = None
                previous = list(records)
                try:
                    proc = subprocess.Popen([args.frpc_bin, "-c", str(path)], start_new_session=True)
                    record = inspect_new_frpc_child(proc, args.frpc_bin, config_paths)
                    if record is None:
                        raise RuntimeError('new FRP client exited before owner registration')
                    records.append(record)
                    # Retain each successful owner before starting another.
                    write_records(args.frpc_pid_file, records)
                except BaseException:
                    # This unreaped child belongs to this Popen invocation;
                    # terminate/wait protects against PID reuse and handles
                    # first-write failure before a registry owner exists.
                    if proc is not None:
                        proc.terminate()
                        try:
                            proc.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            proc.kill()
                            proc.wait(timeout=5)
                    # Only a confirmed terminal child releases the hold. If
                    # reaping or this write fails, future launches fail closed.
                    write_records(args.frpc_pid_file, previous)
                    raise


def check_once(args: argparse.Namespace) -> int:
    cfg = load_json(args.config, {})
    bundle = load_json(args.state, {})
    node = choose_node(cfg, args.node_id)
    runtime = load_json(args.runtime_state, {})
    runtime.setdefault("node_id", args.node_id)
    runtime.setdefault("fail_count", 0)
    runtime.setdefault("last_promotion_at", 0)
    runtime["last_check_at"] = time.time()

    reclaim = load_verified_reclaim_command(args, cfg, bundle, runtime)
    if reclaim:
        demote(args, runtime, args.dry_run)
        runtime["fail_count"] = 0
        runtime["last_promotion_at"] = 0
        runtime["last_decision"] = "reclaimed_primary"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "reclaimed_primary", "node_id": args.node_id, "nonce": reclaim.get("nonce"), "dry_run": args.dry_run}, sort_keys=True))
        return 0

    try:
        maintenance_until = maintenance_deadline(cfg.get("maintenance_until"))
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
    if maintenance_until is not None and runtime["last_check_at"] < maintenance_until:
        runtime["fail_count"] = 0
        runtime["last_decision"] = "maintenance"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "maintenance", "node_id": args.node_id, "maintenance_until": cfg["maintenance_until"]}, sort_keys=True))
        return 0

    if not cfg.get("enabled") or not node.get("enabled", True):
        runtime["last_decision"] = "disabled"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "disabled", "node_id": args.node_id}, sort_keys=True))
        return 0

    timeout = float(cfg.get("public_confirm_timeout_sec") or 3)
    health_url = str(cfg.get("primary_health_url") or "")
    public_url = state_public_url(bundle, cfg)
    primary_ok = http_ok(health_url, timeout)
    if primary_ok:
        runtime["fail_count"] = 0
        runtime["last_decision"] = "primary_ok"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "primary_ok", "node_id": args.node_id, "health_url": health_url}, sort_keys=True))
        return 0

    runtime["fail_count"] = int(runtime.get("fail_count") or 0) + 1
    need = threshold(cfg, node)
    if int(runtime["fail_count"]) < need:
        runtime["last_decision"] = "waiting_rank_threshold"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "waiting_rank_threshold", "fail_count": runtime["fail_count"], "threshold": need, "node_id": args.node_id}, sort_keys=True))
        return 0

    public_health = public_url + "/version" if public_url else ""
    if http_ok(public_health, timeout):
        runtime["fail_count"] = 0
        runtime["last_decision"] = "public_confirm_ok"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "public_confirm_ok", "public_url": public_health, "node_id": args.node_id}, sort_keys=True))
        return 0

    cooldown = int(cfg.get("promotion_cooldown_sec") or 300)
    now = time.time()
    if now - float(runtime.get("last_promotion_at") or 0) < cooldown:
        runtime["last_decision"] = "cooldown"
        save_json(args.runtime_state, runtime)
        print(json.dumps({"ok": True, "decision": "cooldown", "node_id": args.node_id}, sort_keys=True))
        return 0

    runtime["last_decision"] = "promote"
    runtime["last_promotion_at"] = now
    save_json(args.runtime_state, runtime)
    promote(args, cfg, bundle, node, args.dry_run)
    print(json.dumps({"ok": True, "decision": "promote", "node_id": args.node_id, "threshold": need, "public_confirm": public_health, "dry_run": args.dry_run}, sort_keys=True))
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default=os.environ.get("GPTADMIN_FAILOVER_CONFIG_FILE", DEFAULT_CONFIG))
    ap.add_argument("--state", default=os.environ.get("GPTADMIN_FAILOVER_STATE_FILE", DEFAULT_STATE))
    ap.add_argument("--runtime-state", default=DEFAULT_RUNTIME)
    ap.add_argument("--node-id", default=os.environ.get("GPTADMIN_FAILOVER_NODE_ID", ""), required=os.environ.get("GPTADMIN_FAILOVER_NODE_ID", "") == "")
    ap.add_argument("--hub-service", default=os.environ.get("GPTADMIN_FAILOVER_HUB_SERVICE", "gptadmin-hub.service"))
    ap.add_argument("--frpc-service", default=os.environ.get("GPTADMIN_FAILOVER_FRPC_SERVICE", DEFAULT_SERVICE))
    ap.add_argument("--frpc-config", default=os.environ.get("GPTADMIN_FAILOVER_FRPC_CONFIG", DEFAULT_FRPC))
    ap.add_argument("--frpc-bin", default=os.environ.get("GPTADMIN_FAILOVER_FRPC_BIN", "/opt/gptadmin/bin/frpc"))
    ap.add_argument("--frpc-pid-file", default=os.environ.get("GPTADMIN_FAILOVER_FRPC_PID_FILE", DEFAULT_FRPC_PID))
    ap.add_argument("--reclaim-command-file", default=os.environ.get("GPTADMIN_FAILOVER_RECLAIM_COMMAND_FILE", DEFAULT_RECLAIM_COMMAND))
    ap.add_argument("--check-once", action="store_true")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    if not args.check_once:
        raise SystemExit("only --check-once is supported; run from systemd timer")
    return check_once(args)


if __name__ == "__main__":
    raise SystemExit(main())
