#!/usr/bin/env python3
"""Run a redacted public admin password-flow smoke against one or more origins."""

from __future__ import annotations

import argparse
import http.cookiejar
import ipaddress
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


class AdminFlowError(RuntimeError):
    """Raised when a public admin-flow stage fails without disclosing a body."""


_DNS_LABEL = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?")
_LEGACY_NUMERIC_COMPONENT = re.compile(r"(?:0x[0-9A-Fa-f]+|[0-9]+)")


def _is_canonical_host(hostname: str) -> bool:
    """Report whether hostname is an ASCII DNS name or a literal IP address."""

    if not hostname or not hostname.isascii():
        return False
    try:
        ipaddress.ip_address(hostname)
    except ValueError:
        labels = hostname.split(".")
        return (
            len(hostname) <= 253
            and not hostname.endswith(".")
            and not all(label.isdigit() for label in labels)
            and not (
                all(_LEGACY_NUMERIC_COMPONENT.fullmatch(label) for label in labels)
                and any(label.lower().startswith("0x") for label in labels)
            )
            and all(_DNS_LABEL.fullmatch(label) for label in labels)
        )
    return True


def _hub_origin(base_url: str) -> str:
    """Return the Hub origin from an origin or documented admin page URL."""

    if any(ord(character) <= 0x20 or ord(character) == 0x7F for character in base_url):
        raise AdminFlowError("base URL must be an absolute HTTP(S) Hub origin or /admin page")
    try:
        parsed = urllib.parse.urlsplit(base_url)
        parsed.port
        is_invalid = (
            parsed.scheme not in {"http", "https"}
            or not parsed.netloc
            or parsed.username is not None
            or parsed.password is not None
            or parsed.hostname is None
            or not _is_canonical_host(parsed.hostname)
            or (":" in parsed.hostname and not parsed.netloc.startswith("["))
            or (":" not in parsed.hostname and ("[" in parsed.netloc or "]" in parsed.netloc))
            or parsed.netloc.endswith(":")
            or bool(parsed.query)
            or bool(parsed.fragment)
            or parsed.path not in {"", "/", "/admin", "/admin/"}
        )
    except ValueError:
        raise AdminFlowError("base URL must be an absolute HTTP(S) Hub origin or /admin page") from None
    if is_invalid:
        raise AdminFlowError("base URL must be an absolute HTTP(S) Hub origin or /admin page")
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, "", "", ""))


def _request(opener: urllib.request.OpenerDirector, base_url: str, path: str, form: dict[str, str] | None = None) -> tuple[int, bytes]:
    """Execute one bounded request without putting response contents in errors."""

    data = urllib.parse.urlencode(form).encode("utf-8") if form is not None else None
    request = urllib.request.Request(
        base_url.rstrip("/") + path,
        data=data,
        headers={"Accept": "application/json" if path.startswith("/admin/api/") else "text/html"},
        method="POST" if form is not None else "GET",
    )
    try:
        with opener.open(request, timeout=12) as response:
            return response.status, response.read(262144)
    except urllib.error.HTTPError as error:
        return error.code, error.read(262144)
    except urllib.error.URLError as error:
        raise AdminFlowError(f"{path}: transport failure ({type(error.reason).__name__})") from None


def run_admin_user_flow(base_url: str, password: str, require_profiles: bool = False) -> dict[str, Any]:
    """Verify login, cookie refresh, overview, and optionally profile access."""

    base_url = _hub_origin(base_url)
    if not password:
        raise AdminFlowError("password environment variable is empty")

    cookies = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(cookies))
    login_page, body = _request(opener, base_url, "/admin/login")
    if login_page != 200 or b'name="password"' not in body:
        raise AdminFlowError(f"/admin/login: HTTP {login_page} or password form missing")

    login, _ = _request(opener, base_url, "/admin/login", {"password": password, "next": "/admin/"})
    if login != 200 or not any(cookie.name == "gptadmin_admin_session" for cookie in cookies):
        raise AdminFlowError(f"/admin/login: HTTP {login} or session cookie missing")

    refresh, body = _request(opener, base_url, "/admin/")
    if refresh != 200 or b"GPTAdmin Login" in body:
        raise AdminFlowError(f"/admin/: HTTP {refresh} or login page returned after refresh")

    overview, body = _request(opener, base_url, "/admin/api/overview?limit=1")
    try:
        overview_json = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        overview_json = None
    if overview != 200 or not isinstance(overview_json, dict):
        raise AdminFlowError(f"/admin/api/overview: HTTP {overview} or invalid JSON")

    profiles, body = _request(opener, base_url, "/admin/api/access-profiles")
    profiles_ok = False
    if profiles == 200:
        try:
            profiles_ok = isinstance(json.loads(body.decode("utf-8")), dict)
        except (UnicodeDecodeError, json.JSONDecodeError):
            profiles_ok = False
    if require_profiles and not profiles_ok:
        raise AdminFlowError(f"/admin/api/access-profiles: HTTP {profiles} or invalid JSON")

    return {
        "base_url": base_url.rstrip("/"),
        "status": "passed",
        "login": login,
        "refresh": refresh,
        "overview": overview,
        "profiles": profiles,
        "profiles_ok": profiles_ok,
    }


def main(argv: list[str] | None = None) -> int:
    """Run requested origins and print only redacted stage statuses."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", action="append", required=True)
    parser.add_argument("--password-env", default="ADMIN_PASSWORD")
    parser.add_argument("--require-profiles", action="store_true")
    args = parser.parse_args(argv)
    password = os.environ.get(args.password_env, "")
    results: list[dict[str, Any]] = []
    try:
        for base_url in args.base_url:
            results.append(run_admin_user_flow(base_url, password, args.require_profiles))
    except AdminFlowError as error:
        print(json.dumps({"status": "failed", "error": str(error)}, ensure_ascii=False))
        return 1
    print(json.dumps({"status": "passed", "origins": results}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
