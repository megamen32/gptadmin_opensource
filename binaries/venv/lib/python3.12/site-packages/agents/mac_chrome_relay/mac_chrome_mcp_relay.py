#!/usr/bin/env python3
"""
Mac Chrome MCP Relay Agent

Connects a logged-in local Chrome profile to GPTAdmin gptadmin_hub through long polling.
Chrome must be running with remote debugging enabled, for example:

  /Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome \
    --remote-debugging-port=9222 \
    --user-data-dir="$HOME/Library/Application Support/Google/Chrome"

Recommended safer option: use a dedicated profile dir that you log into once:

  mkdir -p "$HOME/.gptadmin-chrome-profile"
  /Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome \
    --remote-debugging-port=9222 \
    --user-data-dir="$HOME/.gptadmin-chrome-profile"
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import sys
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, List, Optional

try:
    from playwright.sync_api import sync_playwright, TimeoutError as PlaywrightTimeoutError
except Exception as e:  # pragma: no cover
    print("ERROR: playwright is required. Install with: python3 -m pip install playwright", file=sys.stderr)
    raise


DEFAULT_HUB = "https://gptadminmcp.bezrabotnyi.com"
DEFAULT_CHROME_CDP = "http://127.0.0.1:9222"
STOP = False


def _stop(*_: Any) -> None:
    global STOP
    STOP = True


signal.signal(signal.SIGINT, _stop)
signal.signal(signal.SIGTERM, _stop)


def http_json(method: str, url: str, token: str, data: Optional[Dict[str, Any]] = None, timeout: int = 70) -> Dict[str, Any]:
    body = None if data is None else json.dumps(data, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=body, method=method.upper())
    req.add_header("Authorization", f"Bearer {token}")
    if body is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            if not raw:
                return {}
            return json.loads(raw)
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        raise RuntimeError(f"HTTP {e.code} {url}: {raw}") from e


@dataclass
class ChromeAgent:
    cdp_url: str
    headless: bool = False

    def __post_init__(self) -> None:
        self._pw = sync_playwright().start()
        self._browser = None

    def close(self) -> None:
        try:
            if self._browser:
                self._browser.close()
        except Exception:
            pass
        try:
            self._pw.stop()
        except Exception:
            pass

    def connect(self):
        if self._browser is None or not self._browser.is_connected():
            self._browser = self._pw.chromium.connect_over_cdp(self.cdp_url)
        return self._browser

    def context(self):
        browser = self.connect()
        if browser.contexts:
            return browser.contexts[0]
        return browser.new_context()

    def pages(self):
        ctx = self.context()
        return ctx.pages

    def page(self, index: int = -1):
        pages = self.pages()
        if pages:
            return pages[index]
        return self.context().new_page()

    def tools_list(self) -> Dict[str, Any]:
        return {
            "tools": [
                {
                    "name": "chrome_tabs",
                    "title": "List Chrome tabs",
                    "description": "List open tabs in the connected logged-in Chrome session.",
                    "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
                    "annotations": {"readOnlyHint": True},
                },
                {
                    "name": "chrome_open",
                    "title": "Open URL",
                    "description": "Open a URL in the logged-in Chrome session.",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "url": {"type": "string"},
                            "new_tab": {"type": "boolean", "default": True},
                            "wait_ms": {"type": "integer", "default": 1500},
                        },
                        "required": ["url"],
                        "additionalProperties": False,
                    },
                    "annotations": {"readOnlyHint": False, "openWorldHint": True, "destructiveHint": False},
                },
                {
                    "name": "chrome_current_page",
                    "title": "Current page snapshot",
                    "description": "Return URL, title and visible text from the active Chrome tab.",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "tab_index": {"type": "integer", "default": -1},
                            "max_chars": {"type": "integer", "default": 12000},
                        },
                        "additionalProperties": False,
                    },
                    "annotations": {"readOnlyHint": True},
                },
                {
                    "name": "chrome_click_text",
                    "title": "Click text",
                    "description": "Click a visible text string or accessible role/name on the active tab.",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "text": {"type": "string"},
                            "tab_index": {"type": "integer", "default": -1},
                            "timeout_ms": {"type": "integer", "default": 5000},
                        },
                        "required": ["text"],
                        "additionalProperties": False,
                    },
                    "annotations": {"readOnlyHint": False, "openWorldHint": False, "destructiveHint": False},
                },
                {
                    "name": "chrome_type",
                    "title": "Type into page",
                    "description": "Type text into the focused element, or into the first matching selector.",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "text": {"type": "string"},
                            "selector": {"type": "string"},
                            "tab_index": {"type": "integer", "default": -1},
                        },
                        "required": ["text"],
                        "additionalProperties": False,
                    },
                    "annotations": {"readOnlyHint": False, "openWorldHint": False, "destructiveHint": False},
                },
                {
                    "name": "chrome_press",
                    "title": "Press keyboard key",
                    "description": "Press a keyboard key such as Enter, Escape, Tab, Meta+L.",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "key": {"type": "string"},
                            "tab_index": {"type": "integer", "default": -1},
                        },
                        "required": ["key"],
                        "additionalProperties": False,
                    },
                    "annotations": {"readOnlyHint": False, "openWorldHint": False, "destructiveHint": False},
                },
                {
                    "name": "chrome_eval",
                    "title": "Evaluate JavaScript",
                    "description": "Evaluate JavaScript on the active tab. Use only for inspection or controlled automation.",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "expression": {"type": "string"},
                            "tab_index": {"type": "integer", "default": -1},
                        },
                        "required": ["expression"],
                        "additionalProperties": False,
                    },
                    "annotations": {"readOnlyHint": False, "openWorldHint": True, "destructiveHint": True},
                },
            ]
        }

    def call_tool(self, name: str, arguments: Dict[str, Any]) -> Dict[str, Any]:
        args = arguments or {}
        if name == "chrome_tabs":
            tabs = []
            for i, p in enumerate(self.pages()):
                try:
                    tabs.append({"index": i, "url": p.url, "title": p.title()})
                except Exception as e:
                    tabs.append({"index": i, "error": str(e)})
            return {"tabs": tabs}

        if name == "chrome_open":
            url = args["url"]
            if not urllib.parse.urlparse(url).scheme:
                url = "https://" + url
            p = self.context().new_page() if args.get("new_tab", True) else self.page()
            p.goto(url, wait_until="domcontentloaded", timeout=45000)
            wait_ms = int(args.get("wait_ms", 1500))
            if wait_ms > 0:
                p.wait_for_timeout(min(wait_ms, 10000))
            return {"url": p.url, "title": p.title()}

        if name == "chrome_current_page":
            p = self.page(int(args.get("tab_index", -1)))
            max_chars = int(args.get("max_chars", 12000))
            text = p.locator("body").inner_text(timeout=10000) if p.locator("body").count() else ""
            return {"url": p.url, "title": p.title(), "text": text[:max_chars], "truncated": len(text) > max_chars}

        if name == "chrome_click_text":
            p = self.page(int(args.get("tab_index", -1)))
            text = args["text"]
            timeout_ms = int(args.get("timeout_ms", 5000))
            try:
                p.get_by_text(text, exact=False).first.click(timeout=timeout_ms)
            except Exception:
                # fallback: accessible name on common roles
                for role in ["button", "link", "textbox", "menuitem", "tab"]:
                    try:
                        p.get_by_role(role, name=text).first.click(timeout=timeout_ms)
                        break
                    except Exception:
                        continue
                else:
                    raise
            p.wait_for_timeout(500)
            return {"ok": True, "url": p.url, "title": p.title()}

        if name == "chrome_type":
            p = self.page(int(args.get("tab_index", -1)))
            text = args["text"]
            selector = args.get("selector")
            if selector:
                p.locator(selector).first.fill(text, timeout=10000)
            else:
                p.keyboard.type(text)
            return {"ok": True}

        if name == "chrome_press":
            p = self.page(int(args.get("tab_index", -1)))
            p.keyboard.press(args["key"])
            p.wait_for_timeout(300)
            return {"ok": True, "url": p.url, "title": p.title()}

        if name == "chrome_eval":
            p = self.page(int(args.get("tab_index", -1)))
            value = p.evaluate(args["expression"])
            return {"value": value, "url": p.url, "title": p.title()}

        raise ValueError(f"unknown local Chrome tool: {name}")


def handle_job(chrome: ChromeAgent, job: Dict[str, Any]) -> Dict[str, Any]:
    method = job.get("method")
    params = job.get("params") or {}
    if method == "tools/list":
        return chrome.tools_list()
    if method == "tools/call":
        return chrome.call_tool(params.get("name"), params.get("arguments") or {})
    raise ValueError(f"unsupported MCP relay method: {method}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hub", default=os.getenv("GPTADMIN_MCP_RELAY_HUB", DEFAULT_HUB))
    parser.add_argument("--token", default=os.getenv("GPTADMIN_MCP_RELAY_TOKEN"))
    parser.add_argument("--agent-id", default=os.getenv("GPTADMIN_MCP_RELAY_AGENT_ID", os.uname().nodename + "-chrome"))
    parser.add_argument("--name", default=os.getenv("GPTADMIN_MCP_RELAY_NAME", "Mac logged-in Chrome"))
    parser.add_argument("--cdp", default=os.getenv("GPTADMIN_CHROME_CDP", DEFAULT_CHROME_CDP))
    parser.add_argument("--poll-timeout", type=int, default=55)
    args = parser.parse_args()

    if not args.token:
        print("ERROR: set GPTADMIN_MCP_RELAY_TOKEN or pass --token", file=sys.stderr)
        return 2

    hub = args.hub.rstrip("/")
    chrome = ChromeAgent(args.cdp)
    try:
        # Fail fast if Chrome CDP is not reachable.
        chrome.connect()
        register_payload = {
            "agent_id": args.agent_id,
            "name": args.name,
            "transport": "stdio",
            "capabilities": ["tools/list", "tools/call", "logged-in-chrome", "playwright-cdp"],
            "meta": {"cdp": args.cdp, "pid_host": os.uname().nodename},
        }
        print(f"Registering {args.agent_id} at {hub} ...", flush=True)
        print(json.dumps(http_json("POST", f"{hub}/mcp-relay/register", args.token, register_payload), ensure_ascii=False), flush=True)

        while not STOP:
            try:
                job = http_json("GET", f"{hub}/mcp-relay/poll/{urllib.parse.quote(args.agent_id)}?timeout={args.poll_timeout}", args.token, timeout=args.poll_timeout + 10)
                if not job:
                    continue
                job_id = job.get("id")
                try:
                    result = handle_job(chrome, job)
                    payload = {"id": job_id, "ok": True, "result": result}
                except Exception as e:
                    payload = {"id": job_id, "ok": False, "error": {"message": str(e), "traceback": traceback.format_exc()[-4000:]}}
                http_json("POST", f"{hub}/mcp-relay/result/{urllib.parse.quote(args.agent_id)}", args.token, payload, timeout=30)
            except Exception as e:
                print(f"loop error: {e}", file=sys.stderr, flush=True)
                time.sleep(3)
    finally:
        chrome.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
