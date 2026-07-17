#!/usr/bin/env python3
"""Public-ingress double used to isolate tunnel and hub failures in Docker E2E."""
from __future__ import annotations

import argparse
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class Handler(BaseHTTPRequestHandler):
    def log_message(self, _format: str, *_args: object) -> None:
        return

    def do_GET(self) -> None:  # noqa: N802
        self.proxy()

    def do_POST(self) -> None:  # noqa: N802
        self.proxy()

    def proxy(self) -> None:
        route = self.server.route_file.read_text().strip() if self.server.route_file.exists() else "primary"  # type: ignore[attr-defined]
        upstream = self.server.routes.get(route, self.server.primary)  # type: ignore[attr-defined]
        body = None
        if self.command == "POST":
            body = self.rfile.read(int(self.headers.get("Content-Length") or "0"))
        headers = {
            name: value
            for name, value in self.headers.items()
            if name.lower() in {"authorization", "content-type", "x-ctl-token", "x-mcp-relay-token"}
        }
        request = urllib.request.Request(upstream + self.path, data=body, headers=headers, method=self.command)
        try:
            # Relay polls are deliberately long-lived (up to 55 seconds), so
            # this tunnel double must not turn an idle poll into a false 502.
            with urllib.request.urlopen(request, timeout=75) as response:  # noqa: S310 - test-local URLs
                data = response.read()
                self.send_response(response.status)
                self.send_header("Content-Type", response.headers.get("Content-Type", "application/json"))
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
        except (OSError, urllib.error.URLError) as error:
            data = str(error).encode()
            self.send_response(502)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", type=int, default=18080)
    parser.add_argument("--primary", default="http://127.0.0.1:9001")
    parser.add_argument("--fallback-one", default="http://127.0.0.1:9101")
    parser.add_argument("--fallback-two", default="http://127.0.0.1:9102")
    parser.add_argument("--route-file", required=True)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.listen), Handler)
    server.primary = args.primary
    server.routes = {
        "fallback": args.fallback_one,
        "fallback-1": args.fallback_one,
        "fallback-2": args.fallback_two,
    }
    server.route_file = Path(args.route_file)
    server.serve_forever()


if __name__ == "__main__":
    main()
