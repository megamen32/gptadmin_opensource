"""Contract and protocol tests for the private full-debug S21 bridge."""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BRIDGE = ROOT / "deploy" / "android-s21-debug-polling-bootstrap.sh"
MAINTAINER = ROOT / "deploy" / "android-s21-shellmcp-polling-maintainer.sh"
PROBE = ROOT / "scripts" / "android_s21_mcp_probe.py"
SERVICE = ROOT / "deploy" / "systemd" / "android-s21-debug-bridge.service"
TIMER = ROOT / "deploy" / "systemd" / "android-s21-debug-bridge.timer"
POLLING_SERVICE = ROOT / "deploy" / "systemd" / "android-s21-gptadmin-bridge.service"
POLLING_TIMER = ROOT / "deploy" / "systemd" / "android-s21-gptadmin-bridge.timer"


def test_bootstrap_uses_phone_local_child_and_preserves_full_debug_surface() -> None:
    script = BRIDGE.read_text(encoding="utf-8")

    assert 'PHONE_MCP_BIND="127.0.0.1"' in script
    assert '"url": "http://127.0.0.1:8080/mcp"' in script
    assert '"transport": "streamable-http"' in script
    assert '"Authorization": "Bearer ${ANDROID_S21_MCP_TOKEN}"' in script
    assert "adb_dev forward" not in script
    assert "adb_dev reverse" not in script
    assert '--ez bearer_token_enabled true' in script
    assert '--ez oauth_enabled false' in script
    assert '--ez tunnel_enabled false' in script
    assert '--ez auto_start_on_boot true' in script
    assert "HUB_PUBLIC_URL" not in script
    assert "PUBLIC_ORIGIN" not in script
    assert "trycloudflare" not in script
    assert "ngrok" not in script

    # The user explicitly requires a full-debug phone: no permission/tool cuts.
    assert '"disabledTools":[],"disabledParams":{}' in script
    assert "pm revoke" not in script
    assert "appops set \"$APP_PACKAGE\" ACCESS_RESTRICTED_SETTINGS allow" in script
    assert "appops set \"$APP_PACKAGE\" ACCESS_RESTRICTED_SETTINGS deny" not in script
    assert "appops set \"$APP_PACKAGE\" ACCESS_RESTRICTED_SETTINGS ignore" not in script
    assert "enabled_accessibility_services" in script
    assert '"disabledTools":["' not in script
    assert "sed -i 's#^export HUB_URL=" not in script
    assert 'PHONE_MCP_CONFIG="$CONFIG_DIR/mcp-supervisor.json"' in script
    assert 'export SHELLMCP_MCP_CONFIG=$PHONE_MCP_CONFIG' in script
    assert '. $PHONE_TOKEN_FILE' in script
    assert 'phone_sh "test -f \'$PHONE_MCP_CONFIG\'"' in script
    assert 'chmod 0755 "$temp_dir"' in script
    assert 'chmod 0644 "$merged"' in script
    assert 'RUN_ONCE_BACKUP="$BASE/backups/run-once.pre-android-mcp-polling.sh"' in script
    assert "cp -p '$RUN_ONCE' '$RUN_ONCE_BACKUP'" in script
    assert "rollback_sha256=" in script
    assert 'ACTION="${1:-apply}"' in script
    assert 'if [[ "$ACTION" == "--rollback" ]]' in script
    assert "rm -f '$PHONE_TOKEN_FILE'" in script
    assert '--es action stop' in script
    assert 'EXPECTED_TOOL_COUNT="58"' in script
    assert (
        'EXPECTED_TOOLS_SHA256="cfaa792fa4a9585a461922fd51d9d61000f7ae4b7273b2d2eac3cb42e8198bfa"'
        in script
    )


def test_bootstrap_pins_the_proven_official_apk_and_never_logs_token() -> None:
    script = BRIDGE.read_text(encoding="utf-8")

    assert 'EXPECTED_APP_VERSION="1.10.0"' in script
    assert (
        'EXPECTED_APK_SHA256="b1a7cf0836c232776449367ab797ecd1c04ee174daa68b948ebcadafb71b53be"'
        in script
    )
    assert "ANDROID_S21_MCP_TOKEN" in script
    assert "token=$ANDROID_S21_MCP_TOKEN" not in script
    assert "echo $ANDROID_S21_MCP_TOKEN" not in script
    assert "printf $ANDROID_S21_MCP_TOKEN" not in script
    assert "ANDROID_S21_VERIFY_APK_SHA256" not in script
    assert "verify_apk" in script
    assert r'cat > \"\$tmp\"' in script
    assert '[[ "$ANDROID_S21_MCP_TOKEN" =~ ^[A-Fa-f0-9]{64}$ ]]' in script


def test_bootstrap_restarts_android_mcp_after_token_change_without_sudo_audit_leak() -> None:
    script = BRIDGE.read_text(encoding="utf-8")

    # A running app keeps its previous in-memory auth configuration until the
    # MCP service is restarted.  The secret-bearing broadcast must not be
    # wrapped in sudo, whose audit line records the complete argv.
    assert "sudo -u" not in script
    assert "setpriv --reuid=" in script
    configure = script.split("configure_phone_mcp()", 1)[1].split(
        "configure_private_transports()", 1
    )[0]
    stop_at = configure.index('--es action stop')
    start_at = configure.index('--es action start')
    assert stop_at < start_at
    assert 'ps -A -o PID,PPID,ARGS' in script
    assert r'\$3==\"sh\" && \$4==\"$RUN\"' in script
    assert "pgrep -f '^sh $RUN$'" not in script
    assert "duplicate_shellmcp_parents" in script
    assert r'\$1==\"shell\" && \$4==\"shellmcp\"' in script
    assert "ss -ltn" not in script
    assert "McpServerService" in script
    assert 'phone_sh "set -e; test -f' in script


def test_no_runtime_usb_bridge_units_are_shipped() -> None:
    assert not SERVICE.exists()
    assert not TIMER.exists()


def test_polling_maintainer_keeps_five_minute_timeout_and_one_parent() -> None:
    script = MAINTAINER.read_text(encoding="utf-8")

    assert "settings put system screen_off_timeout 300000" in script
    assert "screen_off_timeout 15000" not in script
    assert 'ps -A -o PID,PPID,ARGS' in script
    assert "duplicate_shellmcp_parents_or_children" in script
    assert r'\$1==\"shell\" && \$4==\"shellmcp\"' in script
    assert "nohup '$RUN'" in script
    assert "adb_dev reverse --remove tcp:9001" in script
    assert 'adb_dev reverse "tcp:' not in script
    service = POLLING_SERVICE.read_text(encoding="utf-8")
    timer = POLLING_TIMER.read_text(encoding="utf-8")
    assert "outbound polling" in service
    assert "ADB reverse" not in service
    assert "OnUnitActiveSec=300s" in timer


class _MCPHandler(BaseHTTPRequestHandler):
    token = "private-test-token"
    tools = [
        {"name": "android_tap", "inputSchema": {"type": "object"}},
        {"name": "android_get_screen_state", "inputSchema": {"type": "object"}},
        {"name": "android_open_app", "inputSchema": {"type": "object"}},
    ]
    authenticated_initialize_failures = 0

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def do_POST(self) -> None:  # noqa: N802 - stdlib callback name
        if self.headers.get("Authorization") != f"Bearer {self.token}":
            self.send_response(401)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length) or b"{}")
        method = request.get("method")
        if method == "initialize":
            if self.authenticated_initialize_failures:
                type(self).authenticated_initialize_failures -= 1
                self.send_response(503)
                self.end_headers()
                return
            result = {
                "protocolVersion": "2025-03-26",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "test-android", "version": "1"},
            }
            status = 200
        elif method == "notifications/initialized":
            result = None
            status = 202
        elif method == "tools/list":
            result = {"tools": self.tools}
            status = 200
        else:
            result = {}
            status = 200

        payload = b"" if result is None else json.dumps(
            {"jsonrpc": "2.0", "id": request.get("id"), "result": result}
        ).encode()
        self.send_response(status)
        self.send_header("mcp-session-id", "session-test")
        if payload:
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if payload:
            self.wfile.write(payload)


def test_probe_proves_401_and_authenticated_full_tools_digest(tmp_path: Path) -> None:
    _MCPHandler.authenticated_initialize_failures = 0
    server = ThreadingHTTPServer(("127.0.0.1", 0), _MCPHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    token_file = tmp_path / "android.env"
    token_file.write_text("ANDROID_S21_MCP_TOKEN=private-test-token\n", encoding="utf-8")
    names = sorted(tool["name"] for tool in _MCPHandler.tools)
    digest = hashlib.sha256(("\n".join(names) + "\n").encode()).hexdigest()

    try:
        result = subprocess.run(
            [
                sys.executable,
                str(PROBE),
                "--url",
                f"http://127.0.0.1:{server.server_port}/mcp",
                "--token-file",
                str(token_file),
                "--expected-tool-count",
                "3",
                "--expected-tools-sha256",
                digest,
            ],
            check=True,
            capture_output=True,
            text=True,
            timeout=10,
        )
    finally:
        server.shutdown()
        server.server_close()

    receipt = json.loads(result.stdout)
    assert receipt == {
        "authenticated": True,
        "ok": True,
        "session": True,
        "tool_count": 3,
        "tools_sha256": digest,
        "unauthenticated_status": 401,
    }
    assert "private-test-token" not in result.stdout
    assert "private-test-token" not in result.stderr


def test_probe_retries_until_restarted_android_mcp_is_ready(tmp_path: Path) -> None:
    _MCPHandler.authenticated_initialize_failures = 2
    server = ThreadingHTTPServer(("127.0.0.1", 0), _MCPHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    token_file = tmp_path / "android.env"
    token_file.write_text("ANDROID_S21_MCP_TOKEN=private-test-token\n", encoding="utf-8")
    names = sorted(tool["name"] for tool in _MCPHandler.tools)
    digest = hashlib.sha256(("\n".join(names) + "\n").encode()).hexdigest()

    try:
        result = subprocess.run(
            [
                sys.executable,
                str(PROBE),
                "--url",
                f"http://127.0.0.1:{server.server_port}/mcp",
                "--token-file",
                str(token_file),
                "--expected-tool-count",
                "3",
                "--expected-tools-sha256",
                digest,
                "--attempts",
                "3",
                "--retry-delay",
                "0.01",
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=10,
        )
    finally:
        server.shutdown()
        server.server_close()
        _MCPHandler.authenticated_initialize_failures = 0

    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout)["authenticated"] is True
    assert "private-test-token" not in result.stdout
    assert "private-test-token" not in result.stderr


def test_probe_rejects_a_reduced_or_changed_tool_surface(tmp_path: Path) -> None:
    _MCPHandler.authenticated_initialize_failures = 0
    server = ThreadingHTTPServer(("127.0.0.1", 0), _MCPHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    token_file = tmp_path / "android.env"
    token_file.write_text("ANDROID_S21_MCP_TOKEN=private-test-token\n", encoding="utf-8")

    try:
        result = subprocess.run(
            [
                sys.executable,
                str(PROBE),
                "--url",
                f"http://127.0.0.1:{server.server_port}/mcp",
                "--token-file",
                str(token_file),
                "--expected-tool-count",
                "58",
                "--expected-tools-sha256",
                "0" * 64,
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=10,
        )
    finally:
        server.shutdown()
        server.server_close()

    assert result.returncode == 1
    assert "tool surface mismatch" in result.stderr
    assert "private-test-token" not in result.stdout
    assert "private-test-token" not in result.stderr


def test_bridge_rejects_version_match_with_apk_hash_mismatch(tmp_path: Path) -> None:
    fake_adb = tmp_path / "adb"
    fake_adb.write_text(
        """#!/usr/bin/env bash
set -eu
if [[ "${1:-}" == "start-server" ]]; then exit 0; fi
if [[ "${1:-}" == "devices" ]]; then
  if [[ "${2:-}" == "-l" ]]; then
    printf 'List of devices attached\\nFAKES21 device usb:1-1 model:SM_G998B\\n'
  else
    printf 'List of devices attached\\nFAKES21 device\\n'
  fi
  exit 0
fi
if [[ "${1:-}" == "-s" ]]; then shift 2; fi
if [[ "${1:-}" == "shell" && "${2:-}" == "dumpsys" ]]; then
  printf 'versionName=1.10.0\\n'
  exit 0
fi
if [[ "${1:-}" == "shell" && "${2:-}" == "pm" && "${3:-}" == "path" ]]; then
  printf 'package:/data/app/fake/base.apk\\n'
  exit 0
fi
if [[ "${1:-}" == "exec-out" && "${2:-}" == "cat" ]]; then
  printf 'not-the-official-apk'
  exit 0
fi
exit 64
""",
        encoding="utf-8",
    )
    fake_adb.chmod(0o755)
    token_file = tmp_path / "android.env"
    token_file.write_text("ANDROID_S21_MCP_TOKEN=" + "a" * 64 + "\n", encoding="utf-8")
    config_file = tmp_path / "gptadmin.env"
    config_file.write_text("ANDROID_ADB_SERIAL=FAKES21\n", encoding="utf-8")
    env = os.environ.copy()
    env.update(
        {
            "ADB_BIN": str(fake_adb),
            "ADB_RUN_DIRECT": "1",
            "ANDROID_ADB_SERIAL": "FAKES21",
            "ANDROID_S21_MCP_TOKEN_FILE": str(token_file),
            "GPTADMIN_CONFIG_FILE": str(config_file),
        }
    )

    result = subprocess.run(
        [str(BRIDGE)],
        env=env,
        check=False,
        capture_output=True,
        text=True,
        timeout=10,
    )

    assert result.returncode != 0
    assert "unexpected_apk_sha256" in result.stdout
    assert "private-test-token" not in result.stdout
    assert "private-test-token" not in result.stderr
