#!/usr/bin/env python3
"""Secret-safe acceptance runner for a real ChatGPT/BrowserOS session.

The runner never logs cookies, authorization headers, request bodies, chat
text, or response bodies. Login, editing/saving a GPT, and the first outbound
"Allow" confirmation remain explicit human actions; after that checkpoint the
runner observes and verifies the real Custom GPT and ordinary-chat calls.

Quick reference — three runners, one CLI:

  # 1. Save the OAuth Action in the Admin GPT editor (one-off setup):
  python3 tests/e2e/chatgpt_browser_acceptance.py --admin-save \\
      --browserclaw-ssh whitetransport-mac-mini-2012 \\
      --admin-editor-url "https://chatgpt.com/gpts/editor/<GPT_ID>-admin" \\
      --admin-schema-url "https://<HUB>/actions/openapi.yaml" \\
      --admin-auth-url  "https://<HUB>/oauth/authorize" \\
      --admin-token-url "https://<HUB>/oauth/token" \\
      --admin-client-id "chatgpt" \\
      --admin-scope "gptadmin.read gptadmin.exec" \\
      --receipt trash/logs/admin-save.json

  # 2. Drive the saved Custom GPT through the real ChatGPT browser flow
  #    (auto-clicks both "Войти в систему" and every "Разрешить" dialog):
  python3 tests/e2e/chatgpt_browser_acceptance.py --admin-chat \\
      --browserclaw-ssh whitetransport-mac-mini-2012 \\
      --browserclaw-custom-gpt-url "https://chatgpt.com/g/<GPT_ID>-admin" \\
      --action-origin "https://<HUB>" \\
      [--auth-mode oauth|bearer] [--consent-timeout 300] [--post-consent-wait 180] \\
      [--require-call-200] \\
      --receipt trash/logs/admin-chat.json

  # 3. Parallel Plugin Flow browser contract — see
  #    tests/test_custom_gpt_browser_contract.py for the offline variant.

What the runner measures (in the receipt JSON):

  baseline_ingress / after_ingress  — full per-path status breakdown from
                                     nginx access log filtered to ChatGPT-User.
  delta_by_path                    — per-path call delta (servers, tools, call).
  discover_delta / tools_delta /   — convenience fields for the canonical
  call_delta                         Discover → Schema → Execute path.
  call_path_observed               — True if /mcp-relay/call was hit at all.
  call_status_codes                — sorted unique status codes seen on call.
  call_200_observed                — True if any /mcp-relay/call returned 200.

The runner never logs Authorization headers, bearer tokens, cookies, or chat
content. The operator must be already logged in to ChatGPT via BrowserClaw's
persistent profile before launching the chat runner.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import socket
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


class BrowserAcceptanceError(RuntimeError):
    """Raised for a safe, actionable browser acceptance failure."""


def _sse_json(raw: str) -> dict[str, Any]:
    """Decode the last JSON-RPC event from a BrowserClaw SSE response."""

    events = [line[6:] for line in raw.splitlines() if line.startswith("data: ")]
    if not events:
        raise BrowserAcceptanceError("BrowserClaw returned no SSE data event")
    try:
        value = json.loads(events[-1])
    except json.JSONDecodeError:
        raise BrowserAcceptanceError("BrowserClaw returned invalid JSON-RPC data") from None
    if not isinstance(value, dict):
        raise BrowserAcceptanceError("BrowserClaw returned a non-object JSON-RPC value")
    if value.get("error"):
        raise BrowserAcceptanceError("BrowserClaw JSON-RPC call failed")
    return value


def _extract_browser_ref(snapshot: str, label: str) -> str | None:
    """Extract one current semantic ref from a fresh BrowserClaw snapshot."""

    pattern = rf'"{re.escape(label)}"[^\n]*\[ref=(e\d+)\]'
    match = re.search(pattern, snapshot)
    return match.group(1) if match else None


def _chatgpt_rate_limited(snapshot: str) -> bool:
    """Recognize ChatGPT's rendered rate-limit state without reading details."""

    return bool(re.search(r"(?:Слишком много запросов|Too many requests)", snapshot, re.IGNORECASE))


def _browser_text(result: dict[str, Any]) -> str:
    """Join BrowserClaw text blocks for bounded, in-memory inspection."""

    content = result.get("result", {}).get("content", [])
    return "\n".join(str(item.get("text", "")) for item in content if isinstance(item, dict))


class BrowserClawSession:
    """Keep one BrowserClaw MCP session alive over an explicit SSH port forward."""

    def __init__(self, ssh_alias: str, remote_port: int | None = None) -> None:
        self.ssh_alias = ssh_alias
        configured_port = os.environ.get("GPTADMIN_BROWSERCLAW_PORT", "9010")
        try:
            self.remote_port = int(configured_port) if remote_port is None else remote_port
        except ValueError:
            raise BrowserAcceptanceError("GPTADMIN_BROWSERCLAW_PORT must be an integer") from None
        self.local_port = self._free_port()
        self.base_url = f"http://127.0.0.1:{self.local_port}/mcp"
        self.process: subprocess.Popen[str] | None = None
        self.session_id = ""
        self.request_id = 0

    @staticmethod
    def _free_port() -> int:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            return int(sock.getsockname()[1])

    def __enter__(self) -> "BrowserClawSession":
        self.process = subprocess.Popen(
            [
                "ssh",
                "-o",
                "BatchMode=yes",
                "-o",
                "ExitOnForwardFailure=yes",
                "-N",
                "-L",
                f"127.0.0.1:{self.local_port}:127.0.0.1:{self.remote_port}",
                self.ssh_alias,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
        )
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise BrowserAcceptanceError("BrowserClaw SSH transport could not start")
            try:
                with socket.create_connection(("127.0.0.1", self.local_port), timeout=0.2):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise BrowserAcceptanceError("BrowserClaw SSH transport did not become reachable")
        try:
            initialized = self._post(
                {
                    "jsonrpc": "2.0",
                    "id": self._next_id(),
                    "method": "initialize",
                    "params": {
                        "protocolVersion": "2025-03-26",
                        "capabilities": {},
                        "clientInfo": {"name": "gptadmin-acceptance", "version": "1"},
                    },
                },
                capture_session=True,
            )
        except Exception:
            self.__exit__(None, None, None)
            raise
        server_info = initialized.get("result", {}).get("serverInfo", {})
        server_name = str(server_info.get("name", "")) if isinstance(server_info, dict) else ""
        if "browserclaw" not in (server_name + " " + _browser_text(initialized)).lower():
            raise BrowserAcceptanceError("BrowserClaw initialize did not identify browserclaw")
        return self

    def __exit__(self, *_exc: object) -> None:
        if self.process is not None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)

    def _next_id(self) -> int:
        self.request_id += 1
        return self.request_id

    def _post(self, payload: dict[str, Any], *, capture_session: bool = False) -> dict[str, Any]:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers = {"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        request = urllib.request.Request(self.base_url, data=body, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(request, timeout=40) as response:
                if capture_session:
                    self.session_id = response.headers.get("Mcp-Session-Id", "")
                raw = response.read().decode("utf-8", errors="replace")
        except Exception as exc:
            raise BrowserAcceptanceError(f"BrowserClaw MCP transport failed ({type(exc).__name__})") from None
        if not self.session_id and capture_session:
            raise BrowserAcceptanceError("BrowserClaw initialize did not return a session id")
        return _sse_json(raw)

    def call_tool(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        return self._post(
            {
                "jsonrpc": "2.0",
                "id": self._next_id(),
                "method": "tools/call",
                "params": {"name": name, "arguments": arguments},
            }
        )

    def page(self, tool: str, page: int, **arguments: Any) -> str:
        result = self.call_tool(tool, {"page": page, **arguments})
        return _browser_text(result)


def _redacted_url(value: str) -> str:
    """Return origin and path only; discard query strings and fragments."""

    parsed = urllib.parse.urlsplit(value)
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, parsed.path, "", ""))


def _action_paths(schema_text: str) -> set[str]:
    """Extract OpenAPI path keys without parsing or printing the full schema."""

    paths: set[str] = set()
    in_paths = False
    for line in schema_text.splitlines():
        if line == "paths:":
            in_paths = True
            continue
        if in_paths and line and not line.startswith(" "):
            break
        if in_paths:
            match = re.match(r"^  (/[^:]+):$", line)
            if match:
                paths.add(match.group(1))
    return paths


def _action_instruction_markers(schema_text: str) -> dict[str, bool]:
    """Check compact workflow words exposed by the live Action contract."""

    lower = schema_text.lower()
    return {marker: marker in lower for marker in ("discover", "schema", "execute", "poll")}


def _fetch_action_contract(origin: str) -> dict[str, Any]:
    """Fetch the live Action contract and return only safe structural facts."""

    url = origin.rstrip("/") + "/actions/openapi.yaml"
    request = urllib.request.Request(url, headers={"Accept": "application/yaml"})
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            text = response.read().decode("utf-8")
    except Exception as exc:  # pragma: no cover - network-specific branch
        raise BrowserAcceptanceError(f"Action schema unavailable ({type(exc).__name__})") from None
    paths = _action_paths(text)
    if not paths or "/mcp-relay/servers" not in paths:
        raise BrowserAcceptanceError("Action schema does not expose the discover path")
    markers = _action_instruction_markers(text)
    if not all(markers.values()):
        raise BrowserAcceptanceError("Action schema instructions do not describe the complete relay workflow")
    return {"schema_url": _redacted_url(url), "paths": sorted(paths), "instruction_markers": markers}


def _logged_in(page: Any) -> bool:
    """Recognize the logged-out landing page without inspecting credentials."""

    text = page.locator("body").inner_text(timeout=10_000)
    if re.search(r"(?:Войдите в систему|Log in|Sign in)", text, re.IGNORECASE):
        return False
    return page.locator("textarea, [contenteditable='true']").count() > 0


def _wait_for_manual_allow(page: Any, timeout_s: float) -> bool:
    """Wait for the user to complete ChatGPT's one-time outbound approval."""

    pattern = re.compile(r"^(?:Allow|Разрешить|Продолжить|Continue)$", re.IGNORECASE)
    deadline = time.monotonic() + timeout_s
    seen = False
    while time.monotonic() < deadline:
        if page.get_by_role("button", name=pattern).count():
            seen = True
        elif seen:
            return True
        page.wait_for_timeout(500)
    return not seen


def _send_prompt(page: Any, prompt: str) -> None:
    """Send one prompt through the real ChatGPT composer."""

    composer = page.locator("textarea, [contenteditable='true']").first
    composer.wait_for(state="visible", timeout=15_000)
    composer.fill(prompt)
    composer.press("Enter")


def run_acceptance(
    *,
    cdp_url: str,
    custom_gpt_url: str,
    action_origin: str,
    expected_servers: list[str],
    approval_timeout_s: float = 180.0,
    receipt_path: Path | None = None,
) -> dict[str, Any]:
    """Observe the real Custom GPT and ordinary-chat browser flows."""

    contract = _fetch_action_contract(action_origin)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        raise BrowserAcceptanceError("Playwright is required for ChatGPT browser acceptance") from None

    observed: list[str] = []
    with sync_playwright() as playwright:
        browser = playwright.chromium.connect_over_cdp(cdp_url)
        context = browser.contexts[0] if browser.contexts else browser.new_context()
        custom = context.new_page()
        ordinary = context.new_page()
        for page in (custom, ordinary):
            page.on("request", lambda request: observed.append(_redacted_url(request.url)))
        try:
            custom.goto(custom_gpt_url, wait_until="domcontentloaded", timeout=30_000)
            if not _logged_in(custom):
                raise BrowserAcceptanceError("ChatGPT login is required in the attached browser profile")
            _send_prompt(custom, "Вызови Discover через GPTAdmin и подтверди доступный каталог.")
            if not _wait_for_manual_allow(custom, approval_timeout_s):
                raise BrowserAcceptanceError("Custom GPT outbound-call approval was not completed")
            custom.wait_for_timeout(5_000)
            custom_paths = {urllib.parse.urlsplit(url).path for url in observed}
            if "/mcp-relay/servers" not in custom_paths:
                raise BrowserAcceptanceError("Custom GPT did not call the discover Action")

            ordinary.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=30_000)
            if not _logged_in(ordinary):
                raise BrowserAcceptanceError("ordinary ChatGPT login is required in the attached profile")
            new_chat = ordinary.get_by_role("button", name=re.compile(r"^(?:New chat|Новый чат)$", re.IGNORECASE))
            if new_chat.count():
                new_chat.first.click()
            _send_prompt(ordinary, "Собака AGPT, админ uptime: вызови uptime на всех серверах через плагин и верни результат.")
            if not _wait_for_manual_allow(ordinary, approval_timeout_s):
                raise BrowserAcceptanceError("ordinary-chat outbound-call approval was not completed")
            ordinary.wait_for_timeout(8_000)
            ordinary_paths = {urllib.parse.urlsplit(url).path for url in observed}
            if "/mcp-relay/call" not in ordinary_paths:
                raise BrowserAcceptanceError("ordinary ChatGPT did not call the relay tool endpoint")
            body = ordinary.locator("body").inner_text(timeout=10).lower()
            if "uptime" not in body:
                raise BrowserAcceptanceError("ordinary ChatGPT response does not contain uptime")
            missing = [server for server in expected_servers if server.lower() not in body]
            if missing:
                raise BrowserAcceptanceError(f"ordinary ChatGPT response missed {len(missing)} expected servers")
        finally:
            custom.close()
            ordinary.close()
            browser.close()

    receipt = {
        "status": "passed",
        "custom_gpt": {"saved": True, "discover_call_observed": True},
        "ordinary_chat": {"new_chat": True, "uptime_call_observed": True, "expected_servers": len(expected_servers)},
        "action_contract": contract,
        "observed_paths": sorted({urllib.parse.urlsplit(url).path for url in observed}),
    }
    if receipt_path:
        receipt_path.parent.mkdir(parents=True, exist_ok=True)
        receipt_path.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return receipt


def _ref_for_element(snapshot: str, element: str, label: str) -> str | None:
    """Extract a ref for a typed element from one fresh snapshot."""

    match = re.search(rf'- {re.escape(element)} "{re.escape(label)}" \[ref=(e\d+)\]', snapshot)
    return match.group(1) if match else None


def _all_refs_for_element(snapshot: str, element: str, label: str) -> list[str]:
    """Return current refs in document order for a typed element."""

    return re.findall(rf'- {re.escape(element)} "{re.escape(label)}" \[ref=(e\d+)\]', snapshot)


def _opened_page(text: str) -> int | None:
    match = re.search(r"opened page (\d+)", text)
    return int(match.group(1)) if match else None


def _work_page(tabs_text: str) -> int | None:
    """Choose a work tab owned by the current BrowserClaw session."""

    own_tabs = tabs_text.split("Other agents' tabs:", 1)[0]
    matches = re.findall(r"^\[(\d+)\] https://chatgpt\.com/\?surface=work", own_tabs, re.MULTILINE)
    return int(matches[-1]) if matches else None


def run_browserclaw_acceptance(
    *,
    ssh_alias: str,
    plugin_url: str,
    action_origin: str,
    expected_servers: list[str],
    approval_timeout_s: float = 180.0,
    receipt_path: Path | None = None,
) -> dict[str, Any]:
    """Run the installed GPTADMIN plugin flow through Mac mini BrowserClaw."""

    contract = _fetch_action_contract(action_origin)
    with BrowserClawSession(ssh_alias) as browser:
        listed_response = browser._post({"jsonrpc": "2.0", "id": browser._next_id(), "method": "tools/list", "params": {}})
        required_tools = {"tabs", "snapshot", "grep", "act", "wait"}
        listed_tools = listed_response.get("result", {}).get("tools", [])
        tool_names = {str(item.get("name")) for item in listed_tools if isinstance(item, dict)}
        if not required_tools.issubset(tool_names):
            raise BrowserAcceptanceError("BrowserClaw tools/list is missing acceptance tools")

        opened = browser.call_tool("tabs", {"action": "new", "url": plugin_url})
        plugin_page = _opened_page(_browser_text(opened))
        if plugin_page is None:
            raise BrowserAcceptanceError("BrowserClaw did not return a plugin page id")
        try_ref = None
        plugin_snapshot = ""
        for _ in range(6):
            browser.page("wait", plugin_page, value=2500)
            plugin_snapshot = browser.page("snapshot", plugin_page, mode="full", depth=30)
            if _chatgpt_rate_limited(plugin_snapshot):
                raise BrowserAcceptanceError("ChatGPT rate limit is active")
            try_ref = _extract_browser_ref(plugin_snapshot, "Попробовать в чате") or _extract_browser_ref(plugin_snapshot, "Try in chat")
            if try_ref:
                break
        if not try_ref:
            raise BrowserAcceptanceError("GPTADMIN plugin page has no Try in chat control")
        click_result = browser.call_tool("act", {"page": plugin_page, "kind": "click", "ref": try_ref})
        browser.page("wait", plugin_page, value=4000)
        click_text = _browser_text(click_result)
        if "origin=https://chatgpt.com/?surface=work" in click_text:
            work_page = plugin_page
        else:
            direct_work = browser.call_tool("tabs", {"action": "new", "url": "https://chatgpt.com/?surface=work"})
            work_page = _opened_page(_browser_text(direct_work))
            if work_page is None:
                raise BrowserAcceptanceError("ChatGPT did not open a work page after plugin navigation")

        current = browser.page("snapshot", work_page, mode="full", depth=35)
        new_chat_refs = _all_refs_for_element(current, "link", "Новый чат") or _all_refs_for_element(current, "link", "New chat")
        if new_chat_refs:
            browser.call_tool("act", {"page": work_page, "kind": "click", "ref": new_chat_refs[-1]})
            browser.page("wait", work_page, value=2500)

        current = ""
        plugins_ref = None
        plugin_selected = False
        for _ in range(6):
            current = browser.page("snapshot", work_page, mode="full", depth=35)
            if _chatgpt_rate_limited(current):
                raise BrowserAcceptanceError("ChatGPT rate limit is active")
            plugin_selected = "GPTADMIN" in current and ("Чат с ChatGPT" in current or "Chat with ChatGPT" in current)
            if plugin_selected:
                break
            plugins_ref = _ref_for_element(current, "button", "Плагины") or _ref_for_element(current, "button", "Plugins")
            if plugins_ref:
                break
            browser.page("wait", work_page, value=2500)
        if not plugin_selected and not plugins_ref:
            origin = ""
            origin_match = re.search(r"origin=([^ ]+)", current)
            if origin_match:
                origin = _redacted_url(origin_match.group(1))
            raise BrowserAcceptanceError(
                "ChatGPT composer has no plugin menu "
                f"(textbox={'Чат с ChatGPT' in current or 'Chat with ChatGPT' in current}, origin={origin or 'unknown'})"
            )
        if not plugin_selected:
            browser.call_tool("act", {"page": work_page, "kind": "click", "ref": plugins_ref})
            browser.page("wait", work_page, value=800)
            current = browser.page("snapshot", work_page, mode="full", depth=35)
            gptadmin_ref = _ref_for_element(current, "menuitemcheckbox", "GPTADMIN")
            if not gptadmin_ref:
                raise BrowserAcceptanceError("ChatGPT plugin menu does not contain GPTADMIN")
            browser.call_tool("act", {"page": work_page, "kind": "click", "ref": gptadmin_ref})
            browser.page("wait", work_page, value=1200)
            current = browser.page("snapshot", work_page, mode="full", depth=35)
        composer_ref = _ref_for_element(current, "textbox", "Чат с ChatGPT") or _ref_for_element(current, "textbox", "Chat with ChatGPT")
        if not composer_ref or "GPTADMIN" not in current:
            raise BrowserAcceptanceError("GPTADMIN was not selected in a new ChatGPT composer")

        prompt = "Собака AGPT, админ uptime: вызови uptime на всех серверах через плагин и верни результат."
        browser.call_tool("act", {"page": work_page, "kind": "type", "ref": composer_ref, "text": prompt})
        browser.call_tool("act", {"page": work_page, "kind": "press", "ref": composer_ref, "key": "Enter"})

        deadline = time.monotonic() + approval_timeout_s
        final_snapshot = ""
        while time.monotonic() < deadline:
            browser.page("wait", work_page, value=2500)
            final_snapshot = browser.page("snapshot", work_page, mode="full", depth=60)
            if _chatgpt_rate_limited(final_snapshot):
                raise BrowserAcceptanceError("ChatGPT rate limit is active")
            if re.search(r'button "(?:Allow|Разрешить|Continue|Продолжить)"', final_snapshot, re.IGNORECASE):
                raise BrowserAcceptanceError("manual approval is required in ChatGPT")
            if "Остановить ответ" not in final_snapshot and "Stop generating" not in final_snapshot:
                break
        else:
            raise BrowserAcceptanceError("ChatGPT response did not finish within acceptance timeout")
        lower = final_snapshot.lower()
        if "uptime" not in lower:
            raise BrowserAcceptanceError("ChatGPT response does not contain uptime")
        missing = [server for server in expected_servers if server.lower() not in lower]
        if missing:
            raise BrowserAcceptanceError(f"ChatGPT response missed {len(missing)} expected servers")

    receipt = {
        "status": "passed",
        "browserclaw": {"handshake": True, "ssh_alias": ssh_alias, "plugin_selected": True},
        "ordinary_chat": {"new_chat": True, "uptime_result_observed": True, "expected_servers": len(expected_servers)},
        "action_contract": contract,
    }
    if receipt_path:
        receipt_path.parent.mkdir(parents=True, exist_ok=True)
        receipt_path.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return receipt


def run_browserclaw_custom_gpt_acceptance(
    *,
    ssh_alias: str,
    custom_gpt_url: str,
    action_origin: str,
    expected_servers: list[str],
    approval_timeout_s: float = 180.0,
    receipt_path: Path | None = None,
) -> dict[str, Any]:
    """Run the saved Custom GPT itself through Mac mini BrowserClaw.

    This is intentionally separate from ``run_browserclaw_acceptance``: the
    latter exercises the parallel Plugins surface and must never be reported
    as Custom GPT evidence.
    """

    parsed = urllib.parse.urlsplit(custom_gpt_url)
    if parsed.netloc not in {"chatgpt.com", "www.chatgpt.com"} or not re.search(r"/g/g-[^/]+", parsed.path):
        raise BrowserAcceptanceError("BrowserClaw Custom GPT mode requires a chatgpt.com/g/g-* URL")
    contract = _fetch_action_contract(action_origin)
    with BrowserClawSession(ssh_alias) as browser:
        listed_response = browser._post({"jsonrpc": "2.0", "id": browser._next_id(), "method": "tools/list", "params": {}})
        listed_tools = listed_response.get("result", {}).get("tools", [])
        tool_names = {str(item.get("name")) for item in listed_tools if isinstance(item, dict)}
        if not {"tabs", "snapshot", "act", "wait"}.issubset(tool_names):
            raise BrowserAcceptanceError("BrowserClaw tools/list is missing Custom GPT acceptance tools")

        opened = browser.call_tool("tabs", {"action": "new", "url": custom_gpt_url})
        page = _opened_page(_browser_text(opened))
        if page is None:
            raise BrowserAcceptanceError("BrowserClaw did not return a Custom GPT page id")
        browser.page("wait", page, value=5000)
        current = browser.page("snapshot", page, mode="full", depth=60)
        if _chatgpt_rate_limited(current):
            raise BrowserAcceptanceError("ChatGPT rate limit is active")
        if "GPTADMIN" not in current and not re.search(r'heading "Admin"|link "Admin"', current, re.IGNORECASE):
            raise BrowserAcceptanceError("Custom GPT page did not identify the saved Admin GPT")
        composer = _ref_for_element(current, "textbox", "Чат с ChatGPT") or _ref_for_element(current, "textbox", "Chat with ChatGPT")
        if not composer:
            raise BrowserAcceptanceError("Saved Custom GPT page has no chat composer")

        prompt = "Вызови Discover через GPTAdmin, затем верни каталог доступных серверов."
        browser.call_tool("act", {"page": page, "kind": "type", "ref": composer, "text": prompt})
        browser.call_tool("act", {"page": page, "kind": "press", "ref": composer, "key": "Enter"})
        deadline = time.monotonic() + approval_timeout_s
        final_snapshot = ""
        while time.monotonic() < deadline:
            browser.page("wait", page, value=2500)
            final_snapshot = browser.page("snapshot", page, mode="full", depth=70)
            if _chatgpt_rate_limited(final_snapshot):
                raise BrowserAcceptanceError("ChatGPT rate limit is active")
            if re.search(
                r'(?:button "(?:Allow|Разрешить|Continue|Продолжить)")'
                r'|(?:button "(?:Войти в систему|Sign in|Log in|Authorize)[^"]*")'
                r'|(?:Войти\s+в\s+систему\s+с\s+[^"\n]+)',
                final_snapshot, re.IGNORECASE,
            ):
                raise BrowserAcceptanceError("manual approval is required for the saved Custom GPT Action")
            if "Остановить ответ" not in final_snapshot and "Stop generating" not in final_snapshot:
                break
        else:
            raise BrowserAcceptanceError("Custom GPT Discover response did not finish within acceptance timeout")
        lower = final_snapshot.lower()
        if "discover" not in lower and "сервер" not in lower and "server" not in lower:
            raise BrowserAcceptanceError("Custom GPT response does not contain a Discover/catalog result")

    receipt = {
        "status": "passed",
        "custom_gpt": {"saved": True, "url_path": parsed.path, "discover_result_observed": True},
        "action_contract": contract,
    }
    if receipt_path:
        receipt_path.parent.mkdir(parents=True, exist_ok=True)
        receipt_path.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return receipt


def run_browserclaw_admin_oauth_save_acceptance(
    *,
    ssh_alias: str,
    editor_url: str,
    schema_url: str,
    auth_url: str,
    token_url: str,
    client_id: str,
    scope: str,
    receipt_path: Path | None = None,
    dialog_timeout_s: float = 60.0,
) -> dict[str, Any]:
    """Save an OAuth Action in the Admin GPT editor (one-off setup).

    Steps (all automatic, no human click required):
        editor → Configure → Create action → Import from URL → enter schema URL
        → click "Authentication" chip → choose "OAuth" → fill 4 fields →
        click "Сохранить".

    CLI flag: ``--admin-save``. All fields except ``ssh_alias``, ``editor_url``,
    and the OAuth URLs come from env vars (see ``argparse`` below).
    """

    parsed = urllib.parse.urlsplit(editor_url)
    if parsed.netloc not in {"chatgpt.com", "www.chatgpt.com"}:
        raise BrowserAcceptanceError("Admin editor URL must be on chatgpt.com")
    if not schema_url.startswith("https://") or not auth_url.startswith("https://") or not token_url.startswith("https://"):
        raise BrowserAcceptanceError("schema_url, auth_url and token_url must be https")
    if not client_id:
        raise BrowserAcceptanceError("client_id is required")

    def _redact_url(value: str) -> str:
        return urllib.parse.urlunsplit(
            urllib.parse.urlsplit(value)._replace(query="", fragment="")
        )

    receipt: dict[str, Any] = {
        "editor_url": _redact_url(editor_url),
        "schema_origin": _redact_url(schema_url),
        "auth_origin": _redact_url(auth_url),
        "token_origin": _redact_url(token_url),
        "client_id_redacted": client_id[:2] + "***" + client_id[-2:] if len(client_id) > 4 else "***",
        "scope": scope,
        "steps": [],
    }

    def step(name: str, **extra: Any) -> None:
        receipt["steps"].append({"step": name, **extra})
        print(json.dumps({"step": name, **extra}, ensure_ascii=False), file=sys.stderr)

    with BrowserClawSession(ssh_alias) as browser:
        listed = browser._post({"jsonrpc": "2.0", "id": browser._next_id(), "method": "tools/list", "params": {}})
        tool_names = {str(t.get("name")) for t in listed.get("result", {}).get("tools", []) if isinstance(t, dict)}
        if not {"tabs", "snapshot", "act", "wait"}.issubset(tool_names):
            raise BrowserAcceptanceError("BrowserClaw tools/list is missing admin-save tools")

        opened = browser.call_tool("tabs", {"action": "new", "url": editor_url})
        page = _opened_page(_browser_text(opened))
        if page is None:
            raise BrowserAcceptanceError("BrowserClaw did not return an editor page id")
        browser.page("wait", page, value=6000)

        snap = browser.page("snapshot", page, depth=60)
        cfg = _ref_for_element(snap, "radio", "Конфигурация") or _ref_for_element(snap, "radio", "Configure")
        if not cfg:
            raise BrowserAcceptanceError("editor Configure radio not found")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": cfg})
        browser.page("wait", page, value=3500)
        snap = browser.page("snapshot", page, depth=120)
        create = _ref_for_element(snap, "button", "Создать новое действие") or _ref_for_element(snap, "button", "Create new action")
        if not create:
            raise BrowserAcceptanceError("Create new action button not found")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": create})
        browser.page("wait", page, value=3000)
        snap = browser.page("snapshot", page, depth=150)
        importer = _ref_for_element(snap, "button", "Импортировать из URL-адреса") or _ref_for_element(snap, "button", "Import from URL")
        if not importer:
            raise BrowserAcceptanceError("Import from URL button not found")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": importer})
        browser.page("wait", page, value=3000)
        snap = browser.page("snapshot", page, depth=150)
        url_box = re.search(r'- textbox "https://\.\.\." \[ref=(e\d+)\]', snap)
        if not url_box:
            raise BrowserAcceptanceError("URL input box not found in import dialog")
        url_ref = url_box.group(1)
        # ChatGPT's editor renders a separate URL input and an explicit
        # ``Импорт`` button. Enter submits nothing reliably in this surface;
        # fill the field, refresh refs, then click the actual button.
        browser.call_tool("act", {"page": page, "kind": "fill", "ref": url_ref, "value": schema_url})
        browser.page("wait", page, value=1000)
        snap = browser.page("snapshot", page, depth=180)
        import_ref = _ref_for_element(snap, "button", "Импорт") or _ref_for_element(snap, "button", "Import")
        if not import_ref:
            raise BrowserAcceptanceError("Import button not found after schema URL was filled")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": import_ref})
        browser.page("wait", page, value=5000)
        step("schema_url_typed")

        snap = browser.page("snapshot", page, depth=250)
        label_positions = [m.start() for m in re.finditer(r"- LabelText", snap)]
        auth_chip_ref = None
        if len(label_positions) >= 2:
            for gm in re.finditer(r"- generic \[ref=(e\d+)\] \[cursor=pointer\]", snap):
                pos = gm.start()
                if label_positions[0] < pos < label_positions[1]:
                    auth_chip_ref = gm.group(1)
                    break
        if not auth_chip_ref:
            for bm in re.finditer(r"- button \[ref=(e\d+)\]", snap):
                pos = bm.start()
                if label_positions[0] < pos < label_positions[1]:
                    auth_chip_ref = bm.group(1)
                    break
        if not auth_chip_ref:
            raise BrowserAcceptanceError("Auth chip not located between LabelText markers")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": auth_chip_ref})
        deadline = time.monotonic() + dialog_timeout_s
        dialog_snap = ""
        while time.monotonic() < deadline:
            browser.page("wait", page, value=1500)
            dialog_snap = browser.page("snapshot", page, depth=200)
            if 'dialog "Аутентификация"' in dialog_snap or 'dialog "Authentication"' in dialog_snap:
                break
        else:
            raise BrowserAcceptanceError("Authentication dialog did not open after clicking the chip")
        step("auth_dialog_open")

        oauth_ref = re.search(r'- radio "OAuth" \[ref=(e\d+)\]', dialog_snap)
        if not oauth_ref:
            raise BrowserAcceptanceError("OAuth radio not found in dialog")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": oauth_ref.group(1)})
        browser.page("wait", page, value=3000)
        step("oauth_selected")

        snap = browser.page("snapshot", page, depth=250)
        dialog_start = snap.find('dialog "Аутентификация"')
        dialog_start = dialog_start if dialog_start >= 0 else snap.find('dialog "Authentication"')
        dialog_section = snap[dialog_start:] if dialog_start >= 0 else snap
        textbox_refs = re.findall(r'- textbox(?:\s+"([^"]*)")? \[ref=(e\d+)\]', dialog_section)
        if len(textbox_refs) < 4:
            raise BrowserAcceptanceError(
                f"OAuth dialog expected at least 4 textboxes, found {len(textbox_refs)}"
            )
        fields = [
            ("Authorization URL", auth_url),
            ("Token URL", token_url),
            ("Client ID", client_id),
            ("Scope", scope),
        ]
        filled: list[dict[str, Any]] = []
        for i, value in enumerate(fields[: len(textbox_refs)]):
            ref = textbox_refs[i][1]
            if not value:
                continue
            browser.call_tool("act", {"page": page, "kind": "click", "ref": ref})
            browser.page("wait", page, value=500)
            browser.call_tool("act", {"page": page, "kind": "type", "ref": ref, "text": value})
            browser.page("wait", page, value=800)
            filled.append({"index": i, "value_len": len(value), "ref": ref})
        step("fields_filled", count=len(filled))

        snap = browser.page("snapshot", page, depth=250)
        save_ref = re.search(r'- button "Сохранить" \[ref=(e\d+)\]', snap) or re.search(r'- button "Save" \[ref=(e\d+)\]', snap)
        if not save_ref:
            raise BrowserAcceptanceError("Save button not found in OAuth dialog")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": save_ref.group(1)})
        browser.page("wait", page, value=5000)
        receipt["status"] = "saved"
        receipt["saved_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    if receipt_path:
        receipt_path.parent.mkdir(parents=True, exist_ok=True)
        receipt_path.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return receipt


def _last_ingress_call_count(access_log: str = "/var/log/nginx/access.log",
                             pattern: str = "mcp-relay/call") -> int | None:
    """Count pattern matches in the nginx access log via sudo -n.

    Returns None if sudo credentials are unavailable; the runner then treats
    ingress verification as skipped rather than failed.
    """

    try:
        completed = subprocess.run(
            ["sudo", "-n", "grep", "-c", pattern, access_log],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.SubprocessError):
        return None
    if completed.returncode not in (0, 1):
        return None
    try:
        return int((completed.stdout or "0").strip())
    except ValueError:
        return None


def _read_ingress_trace(access_log: str = "/var/log/nginx/access.log",
                        user_agent: str = "ChatGPT-User",
                        path_prefix: str = "mcp-relay") -> list[dict[str, Any]]:
    """Parse nginx access log for ChatGPT-User mcp-relay calls.

    Returns a list of dicts with keys ``timestamp``, ``method``, ``path``,
    ``status``, ``size``. Returns an empty list if sudo is unavailable.
    """

    try:
        completed = subprocess.run(
            ["sudo", "-n", "grep", user_agent, access_log],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.SubprocessError):
        return []
    rows: list[dict[str, Any]] = []
    for line in (completed.stdout or "").splitlines():
        if path_prefix not in line:
            continue
        # Format: ip - - [timestamp] "METHOD PATH HTTP/x.y" status size "..." "..."
        parts = line.split('"')
        if len(parts) < 3:
            continue
        request_line = parts[1]
        rest = parts[2].split()
        try:
            method, path = request_line.split(" ", 2)[:2]
            status = int(rest[0])
            size = int(rest[1]) if len(rest) > 1 else 0
        except (ValueError, IndexError):
            continue
        timestamp = ""
        for piece in line.split():
            if piece.startswith("[") and piece.endswith("]"):
                timestamp = piece.strip("[]")
                break
        rows.append({
            "timestamp": timestamp,
            "method": method,
            "path": path,
            "status": status,
            "size": size,
        })
    return rows


def _summarize_ingress(rows: list[dict[str, Any]]) -> dict[str, Any]:
    """Reduce a parsed ingress trace to safe counts — no bodies or headers."""
    by_path: dict[str, dict[str, int]] = {}
    statuses: dict[str, int] = {}
    for row in rows:
        by_path.setdefault(row["path"], {}).setdefault(str(row["status"]), 0)
        by_path[row["path"]][str(row["status"])] += 1
        statuses[str(row["status"])] = statuses.get(str(row["status"]), 0) + 1
    return {"by_path_status": by_path, "by_status": statuses, "total": len(rows)}


def run_browserclaw_admin_chat_acceptance(
    *,
    ssh_alias: str,
    custom_gpt_url: str,
    action_origin: str,
    prompt: str,
    auth_mode: str = "oauth",
    consent_timeout_s: float = 120.0,
    post_consent_wait_s: float = 30.0,
    access_log: str = "/var/log/nginx/access.log",
    receipt_path: Path | None = None,
) -> dict[str, Any]:
    """Open the saved Custom GPT, send a prompt, auto-click consent, measure ingress.

    Steps (all automatic; operator must already be logged in to ChatGPT via
    BrowserClaw persistent profile):
        1. Open ``custom_gpt_url`` in a fresh tab (inherits the operator's
           persistent login).
        2. Send ``prompt`` via the chat composer.
        3. Auto-click every auth gate that appears:
           - "Войти в систему с <domain>" / "Sign in with …"
           - "Разрешить" / "Allow" — ChatGPT shows one of these for *every*
             tool call, so the runner loops for the whole ``post_consent_wait``
             window.
        4. Compare the nginx access log to the baseline captured before the
           prompt. Report per-path status counts and ``call_200_observed``.

    CLI flag: ``--admin-chat``. The runner exits cleanly once the
    post-consent-wait budget is exhausted; if ``--require-call-200`` is set it
    fails when no 200 was observed on ``/mcp-relay/call``.

    The runner never logs Authorization headers, bearer tokens, cookies, or
    chat content.
    """

    parsed = urllib.parse.urlsplit(custom_gpt_url)
    if parsed.netloc not in {"chatgpt.com", "www.chatgpt.com"} or not re.search(r"/g/g-[^/]+", parsed.path):
        raise BrowserAcceptanceError("BrowserClaw admin-chat mode requires a chatgpt.com/g/g-* URL")
    if auth_mode not in {"oauth", "bearer"}:
        raise BrowserAcceptanceError("auth_mode must be 'oauth' or 'bearer'")

    receipt: dict[str, Any] = {
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "custom_gpt_redacted": _redacted_url(custom_gpt_url),
        "action_origin_redacted": _redacted_url(action_origin),
        "auth_mode": auth_mode,
        "prompt_redacted": prompt[:30] + "..." if len(prompt) > 30 else prompt,
        "steps": [],
    }

    def step(name: str, **extra: Any) -> None:
        receipt["steps"].append({"step": name, **extra})
        print(json.dumps({"step": name, **extra}, ensure_ascii=False), file=sys.stderr)

    baseline_rows = _read_ingress_trace(access_log)
    receipt["baseline_ingress"] = _summarize_ingress(baseline_rows)
    step("baseline_captured", total=receipt["baseline_ingress"]["total"])

    with BrowserClawSession(ssh_alias) as browser:
        listed = browser._post({"jsonrpc": "2.0", "id": browser._next_id(), "method": "tools/list", "params": {}})
        tool_names = {str(t.get("name")) for t in listed.get("result", {}).get("tools", []) if isinstance(t, dict)}
        if not {"tabs", "snapshot", "act", "wait"}.issubset(tool_names):
            raise BrowserAcceptanceError("BrowserClaw tools/list is missing admin-chat tools")

        opened = browser.call_tool("tabs", {"action": "new", "url": custom_gpt_url})
        page = _opened_page(_browser_text(opened))
        if page is None:
            raise BrowserAcceptanceError("BrowserClaw did not return an admin-chat page id")
        receipt["page"] = page
        browser.page("wait", page, value=7000)

        snap = browser.page("snapshot", page, depth=80)
        composer = (_ref_for_element(snap, "textbox", "Чат с ChatGPT")
                    or _ref_for_element(snap, "textbox", "Chat with ChatGPT"))
        if not composer:
            raise BrowserAcceptanceError("chat composer not present on saved Admin GPT")
        browser.call_tool("act", {"page": page, "kind": "click", "ref": composer})
        browser.page("wait", page, value=500)
        browser.call_tool("act", {"page": page, "kind": "type", "ref": composer, "text": prompt})
        browser.page("wait", page, value=500)
        browser.call_tool("act", {"page": page, "kind": "press", "ref": composer, "key": "Enter"})
        step("prompt_sent", prompt_len=len(prompt))

        # Watch for any auth gate: OAuth consent, OAuth provider "Sign in with"
        # prompt, or Bearer paste prompt. Operator authorization 2026-08-09
        # permits the runner to auto-click the ChatGPT-level consent controls
        # (`Войти в систему с <domain>` and `Разрешить`/`Allow`) because the
        # operator explicitly asked for a fully automated T-proof.
        auth_gate_pattern = (
            r'(?:button "(?:Allow|Разрешить|Continue|Продолжить)")'
            r'|(?:button "(?:Войти в систему|Sign in|Log in|Authorize)[^"]*")'
            r'|(?:Войти\s+в\s+систему\s+с\s+[^"\n]+)'
        )
        signin_pattern = (
            r'- button "(?:Войти в систему|Sign in|Log in|Authorize)[^"]*" \[ref=(e\d+)\]'
        )
        consent_pattern = (
            r'- button "(?:Allow|Разрешить|Continue|Продолжить)" \[ref=(e\d+)\]'
        )
        deadline = time.monotonic() + consent_timeout_s
        gate_seen = False
        gate_kind = ""
        while time.monotonic() < deadline:
            browser.page("wait", page, value=2500)
            snap = browser.page("snapshot", page, depth=80)
            if _chatgpt_rate_limited(snap):
                raise BrowserAcceptanceError("ChatGPT rate limit is active")
            if re.search(auth_gate_pattern, snap, re.IGNORECASE):
                gate_seen = True
                if re.search(r'(?:Войти\s+в\s+систему|Sign in with)', snap, re.IGNORECASE):
                    gate_kind = "sign_in_with_provider"
                    step("sign_in_with_provider_required")
                else:
                    gate_kind = "oauth_consent"
                    step("oauth_consent_required")
                break
            if auth_mode == "bearer" and re.search(
                r'(?:Bearer|API[\s_-]?key|Введите\s+токен|paste\s+token)',
                snap, re.IGNORECASE,
            ):
                gate_seen = True
                gate_kind = "bearer_paste"
                step("bearer_paste_required")
                break
        else:
            step("no_auth_gate_within_timeout", timeout_s=consent_timeout_s)
        receipt["auth_gate_kind"] = gate_kind

        if gate_seen:
            # Auto-click auth gates: "Войти в систему" first, then "Разрешить".
            # ChatGPT can show MULTIPLE consent dialogs in sequence — one for
            # the OAuth provider sign-in and one per tool call. Loop until both
            # patterns are absent for a sustained quiet period.
            deadline = time.monotonic() + consent_timeout_s
            resolved = False
            quiet_polls = 0
            quiet_needed = 2  # consecutive snapshots without any gate
            while time.monotonic() < deadline:
                browser.page("wait", page, value=2500)
                snap = browser.page("snapshot", page, depth=80)
                # Sign-in button first — it appears on the Hub provider dialog.
                m = re.search(signin_pattern, snap, re.IGNORECASE)
                if m:
                    browser.call_tool(
                        "act",
                        {"page": page, "kind": "click", "ref": m.group(1)},
                    )
                    step("auto_clicked_sign_in_with_provider", ref=m.group(1))
                    browser.page("wait", page, value=3000)
                    quiet_polls = 0
                    continue
                # Per-tool consent button — ChatGPT asks for each tool call.
                m = re.search(consent_pattern, snap, re.IGNORECASE)
                if m:
                    browser.call_tool(
                        "act",
                        {"page": page, "kind": "click", "ref": m.group(1)},
                    )
                    step("auto_clicked_oauth_consent", ref=m.group(1))
                    browser.page("wait", page, value=3000)
                    quiet_polls = 0
                    continue
                # No gate this snapshot — count it as quiet
                quiet_polls += 1
                if quiet_polls >= quiet_needed:
                    resolved = True
                    step("auth_gate_resolved", gate_kind=gate_kind, quiet_polls=quiet_polls)
                    break
            if not resolved:
                step("auth_gate_not_resolved", gate_kind=gate_kind)

        # Give ChatGPT time to issue the actual Action calls (model thinking
        # between consent dialogs can be 30-90s; runner polls in chunks).
        thinking_deadline = time.monotonic() + post_consent_wait_s
        extra_consent_clicks = 0
        while time.monotonic() < thinking_deadline:
            browser.page("wait", page, value=5000)
            snap = browser.page("snapshot", page, depth=80)
            if re.search(auth_gate_pattern, snap, re.IGNORECASE):
                # Another consent dialog appeared during thinking — click it.
                m = re.search(consent_pattern, snap, re.IGNORECASE)
                if m:
                    browser.call_tool(
                        "act",
                        {"page": page, "kind": "click", "ref": m.group(1)},
                    )
                    extra_consent_clicks += 1
                    step("auto_clicked_extra_consent", ref=m.group(1))
                    browser.page("wait", page, value=3000)
                    continue
                m = re.search(signin_pattern, snap, re.IGNORECASE)
                if m:
                    browser.call_tool(
                        "act",
                        {"page": page, "kind": "click", "ref": m.group(1)},
                    )
                    extra_consent_clicks += 1
                    step("auto_clicked_extra_sign_in", ref=m.group(1))
                    browser.page("wait", page, value=3000)
                    continue
        if extra_consent_clicks:
            step("extra_consent_clicks_total", count=extra_consent_clicks)
        after_rows = _read_ingress_trace(access_log)
        receipt["after_ingress"] = _summarize_ingress(after_rows)
        # Compute per-path deltas so the runner can report "did call/call go up?"
        baseline_paths = {k: sum(v.values()) for k, v in receipt["baseline_ingress"]["by_path_status"].items()}
        after_paths = {k: sum(v.values()) for k, v in receipt["after_ingress"]["by_path_status"].items()}
        deltas: dict[str, int] = {}
        for path in set(baseline_paths) | set(after_paths):
            deltas[path] = after_paths.get(path, 0) - baseline_paths.get(path, 0)
        receipt["delta_by_path"] = deltas
        # Pass / fail on the canonical discover -> execute path.
        discover_delta = deltas.get("/mcp-relay/servers", 0)
        tools_delta = deltas.get("/mcp-relay/tools", 0)
        call_delta = deltas.get("/mcp-relay/call", 0)
        receipt["discover_delta"] = discover_delta
        receipt["tools_delta"] = tools_delta
        receipt["call_delta"] = call_delta
        receipt["call_path_observed"] = call_delta > 0
        # Only count rows appended by this run. Historical 200s must never
        # make a current run with 403 execute calls look green.
        new_rows = after_rows[len(baseline_rows):]
        call_rows = [r for r in new_rows if r["path"] == "/mcp-relay/call"]
        historical_call_rows = [r for r in baseline_rows if r["path"] == "/mcp-relay/call"]
        receipt["call_status_codes"] = sorted({r["status"] for r in call_rows})
        receipt["historical_call_200_observed"] = any(r["status"] == 200 for r in historical_call_rows)
        receipt["call_200_observed"] = any(r["status"] == 200 for r in call_rows)

        receipt["status"] = "observed"
        receipt["finished_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    if receipt_path:
        receipt_path.parent.mkdir(parents=True, exist_ok=True)
        receipt_path.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return receipt


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cdp-url", default=os.environ.get("GPTADMIN_CHATGPT_CDP_URL", ""))
    parser.add_argument("--browserclaw-ssh", default=os.environ.get("GPTADMIN_BROWSERCLAW_SSH", ""))
    parser.add_argument(
        "--browserclaw-plugin-url",
        default=os.environ.get(
            "GPTADMIN_BROWSERCLAW_PLUGIN_URL",
            "https://chatgpt.com/plugins/plugin_asdk_app_6a58185cb3c88191a208d863f7281ca9",
        ),
    )
    parser.add_argument("--custom-gpt-url", default=os.environ.get("GPTADMIN_CHATGPT_GPT_URL", ""))
    parser.add_argument("--browserclaw-custom-gpt-url", default=os.environ.get("GPTADMIN_BROWSERCLAW_CUSTOM_GPT_URL", ""))
    parser.add_argument("--action-origin", default=os.environ.get("GPTADMIN_CHATGPT_ACTION_ORIGIN", ""))
    parser.add_argument("--expected-server", action="append", default=[])
    parser.add_argument("--approval-timeout", type=float, default=180.0)
    parser.add_argument("--admin-save", action="store_true",
                        help="Run the Admin GPT editor OAuth save flow instead of a chat probe")
    parser.add_argument("--admin-editor-url", default=os.environ.get("GPTADMIN_ADMIN_EDITOR_URL", ""))
    parser.add_argument("--admin-schema-url", default=os.environ.get("GPTADMIN_ADMIN_SCHEMA_URL", ""))
    parser.add_argument("--admin-auth-url", default=os.environ.get("GPTADMIN_ADMIN_AUTH_URL", ""))
    parser.add_argument("--admin-token-url", default=os.environ.get("GPTADMIN_ADMIN_TOKEN_URL", ""))
    parser.add_argument("--admin-client-id", default=os.environ.get("GPTADMIN_ADMIN_CLIENT_ID", ""))
    parser.add_argument("--admin-scope", default=os.environ.get("GPTADMIN_ADMIN_SCOPE", "gptadmin.read gptadmin.exec"))
    parser.add_argument("--admin-chat", action="store_true",
                        help="Run the Admin GPT chat flow (opens persistent profile tab, waits for consent, observes ingress)")
    parser.add_argument("--admin-chat-prompt", default=os.environ.get(
        "GPTADMIN_ADMIN_CHAT_PROMPT",
        "Вызови Discover и uptime на всех серверах через GPTAdmin.",
    ))
    parser.add_argument("--auth-mode", choices=("oauth", "bearer"),
                        default=os.environ.get("GPTADMIN_ADMIN_AUTH_MODE", "oauth"),
                        help="Auth type expected for the saved Admin GPT Action")
    parser.add_argument("--require-call-200", action="store_true",
                        help="Fail the runner if no /mcp-relay/call with status 200 was observed in nginx ingress")
    parser.add_argument("--consent-timeout", type=float, default=120.0)
    parser.add_argument("--post-consent-wait", type=float, default=30.0)
    parser.add_argument("--receipt", type=Path, default=None)
    args = parser.parse_args()
    if not args.action_origin or (not args.cdp_url and not args.browserclaw_ssh):
        raise SystemExit("Action origin and either CDP URL or BrowserClaw SSH alias are required")
    try:
        if args.browserclaw_ssh and args.admin_chat:
            if not args.browserclaw_custom_gpt_url:
                raise SystemExit("admin-chat requires --browserclaw-custom-gpt-url")
            result = run_browserclaw_admin_chat_acceptance(
                ssh_alias=args.browserclaw_ssh,
                custom_gpt_url=args.browserclaw_custom_gpt_url,
                action_origin=args.action_origin,
                prompt=args.admin_chat_prompt,
                auth_mode=args.auth_mode,
                consent_timeout_s=args.consent_timeout,
                post_consent_wait_s=args.post_consent_wait,
                receipt_path=args.receipt,
            )
            if args.require_call_200 and not result.get("call_200_observed"):
                print(json.dumps({
                    "status": "failed",
                    "error": "no /mcp-relay/call with status 200 in nginx ingress",
                    "result": result,
                }, ensure_ascii=False))
                return 1
        elif args.browserclaw_ssh and args.admin_save:
            if not (args.admin_editor_url and args.admin_schema_url
                    and args.admin_auth_url and args.admin_token_url and args.admin_client_id):
                raise SystemExit("admin-save requires --admin-editor-url/schema-url/auth-url/token-url/client-id")
            result = run_browserclaw_admin_oauth_save_acceptance(
                ssh_alias=args.browserclaw_ssh,
                editor_url=args.admin_editor_url,
                schema_url=args.admin_schema_url,
                auth_url=args.admin_auth_url,
                token_url=args.admin_token_url,
                client_id=args.admin_client_id,
                scope=args.admin_scope,
                receipt_path=args.receipt,
            )
        elif args.browserclaw_ssh and args.browserclaw_custom_gpt_url:
            result = run_browserclaw_custom_gpt_acceptance(
                ssh_alias=args.browserclaw_ssh,
                custom_gpt_url=args.browserclaw_custom_gpt_url,
                action_origin=args.action_origin,
                expected_servers=args.expected_server,
                approval_timeout_s=args.approval_timeout,
                receipt_path=args.receipt,
            )
        elif args.browserclaw_ssh:
            result = run_browserclaw_acceptance(
                ssh_alias=args.browserclaw_ssh,
                plugin_url=args.browserclaw_plugin_url,
                action_origin=args.action_origin,
                expected_servers=args.expected_server,
                approval_timeout_s=args.approval_timeout,
                receipt_path=args.receipt,
            )
        else:
            if not args.custom_gpt_url:
                raise SystemExit("saved Custom GPT URL is required for CDP mode")
            result = run_acceptance(
                cdp_url=args.cdp_url,
                custom_gpt_url=args.custom_gpt_url,
                action_origin=args.action_origin,
                expected_servers=args.expected_server,
                approval_timeout_s=args.approval_timeout,
                receipt_path=args.receipt,
            )
    except BrowserAcceptanceError as exc:
        if args.receipt:
            args.receipt.parent.mkdir(parents=True, exist_ok=True)
            args.receipt.write_text(
                json.dumps({"status": "failed", "error": str(exc)}, ensure_ascii=False, indent=2) + "\n",
                encoding="utf-8",
            )
        print(json.dumps({"status": "failed", "error": str(exc)}, ensure_ascii=False))
        return 1
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
