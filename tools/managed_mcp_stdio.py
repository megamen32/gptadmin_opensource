#!/usr/bin/env python3
"""Bounded stdio transport for an existing GPTAdmin managed HTTP connection.

Credentials stay in the owner's protected upstream JSON. This is a native MCP
configuration extension, not a replacement server or an unscoped endpoint.
"""
import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

MAX_BYTES = 8 * 1024 * 1024
TIMEOUT_SECONDS = 90
EXPIRED = "connection session expired or unavailable; initialize again"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class UpstreamFailure(Exception):
    def __init__(self, status=None, detail=None):
        self.status = status
        self.detail = detail


class ManagedTransport:
    def __init__(self, upstream):
        self.url = upstream["url"]
        url = urllib.parse.urlsplit(self.url)
        if (url.scheme not in ("https", "http") or not url.hostname or url.username
                or url.password or url.query or url.fragment
                or not url.path.startswith("/connection/") or not url.path.endswith("/mcp")
                or (url.scheme == "http" and url.hostname not in ("localhost", "127.0.0.1", "::1"))):
            raise ValueError("Expected the existing HTTPS managed MCP URL")
        self.headers = dict(upstream.get("headers", {}))
        if any(k.lower() in ("mcp-session-id", "mcp-method", "mcp-name") for k in self.headers):
            raise ValueError("Upstream credentials cannot include connection routing headers")
        self.opener = urllib.request.build_opener(NoRedirect)
        self.session_id = None
        self.initialize = None
        self.protocol = "2025-03-26"

    def post(self, request, *, initializing=False):
        headers = {**self.headers, "Content-Type": "application/json",
                   "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": self.protocol,
                   "Mcp-Method": request["method"]}
        if request["method"] == "tools/call":
            headers["Mcp-Name"] = request["params"]["name"]
        if self.session_id and not initializing:
            headers["Mcp-Session-Id"] = self.session_id
        body = json.dumps(request).encode()
        if len(body) > MAX_BYTES:
            raise UpstreamFailure()
        try:
            response = self.opener.open(urllib.request.Request(self.url, data=body, headers=headers),
                                        timeout=TIMEOUT_SECONDS)
        except urllib.error.HTTPError as error:
            try:
                data = json.loads(error.read(MAX_BYTES + 1))
            except (ValueError, OSError):
                data = {}
            finally:
                error.close()
            raise UpstreamFailure(error.code, data.get("detail") if isinstance(data, dict) else None) from None
        except (OSError, TimeoutError, urllib.error.URLError):
            raise UpstreamFailure() from None
        with response:
            try:
                raw = response.read(MAX_BYTES + 1)
                if len(raw) > MAX_BYTES:
                    raise UpstreamFailure()
                result = json.loads(raw) if raw else None
            except (ValueError, OSError):
                raise UpstreamFailure() from None
            if initializing and result and "result" in result:
                session_id = response.headers.get("Mcp-Session-Id")
                if not session_id:
                    raise UpstreamFailure()
                self.session_id = session_id
                self.protocol = result["result"].get("protocolVersion", self.protocol)
            return result

    def request(self, request):
        if request.get("method") == "initialize":
            self.initialize = request
            self.protocol = request.get("params", {}).get("protocolVersion", self.protocol)
            return self.post(request, initializing=True)
        try:
            return self.post(request)
        except UpstreamFailure as error:
            # This exact managed-endpoint refusal occurs before child dispatch.
            # Any other HTTP/network/response failure can follow admission and
            # must never replay a tool. Keep request ID and arguments unchanged.
            if (error.status != 404 or error.detail != EXPIRED
                    or self.initialize is None or self.session_id is None):
                raise
            initialized = self.post(self.initialize, initializing=True)
            if not initialized or "result" not in initialized:
                raise UpstreamFailure()
            self.post({"jsonrpc": "2.0", "method": "notifications/initialized"})
            print('{"event":"managed_mcp_reinitialized","reason":"pre_dispatch_expiry"}',
                  file=sys.stderr, flush=True)
            # Deliberately outside the recovery catch: at most one attempt.
            return self.post(request)

    def close(self):
        if not self.session_id:
            return
        try:
            with self.opener.open(urllib.request.Request(self.url, method="DELETE", headers={
                    **self.headers, "Mcp-Session-Id": self.session_id,
                    "MCP-Protocol-Version": self.protocol}), timeout=5) as response:
                confirmed = response.status == 204
        except (OSError, urllib.error.URLError):
            confirmed = False
        if not confirmed:
            print('{"event":"managed_mcp_close_unconfirmed"}', file=sys.stderr, flush=True)


def serve(transport):
    while True:
        line = sys.stdin.buffer.readline(MAX_BYTES + 1)
        if not line:
            return
        if len(line) > MAX_BYTES:
            raise ValueError("MCP frame exceeds the finite transport limit")
        request = json.loads(line)
        try:
            result = transport.request(request)
        except UpstreamFailure as error:
            result = {"jsonrpc": "2.0", "id": request.get("id"), "error": {
                "code": -32000,
                "message": "Managed MCP response unavailable; request was not retried"
                + (" (HTTP %s)" % error.status if error.status else "")}}
        if "id" in request and result is not None:
            print(json.dumps(result), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--upstream", required=True, type=Path)
    args = parser.parse_args()
    transport = ManagedTransport(json.loads(args.upstream.read_text()))
    try:
        serve(transport)
    finally:
        transport.close()


if __name__ == "__main__":
    main()
