#!/usr/bin/env python3
"""Validate the private HAOS quorum config and print only its local public URL."""
import json
import os
from pathlib import Path
import stat
import sys
from urllib.parse import urlsplit


def local_public_url(path: Path) -> str:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise ValueError("quorum config must be a private regular file")
        with os.fdopen(fd, encoding="utf-8") as source:
            fd = -1
            data = json.load(source)
    finally:
        if fd >= 0:
            os.close(fd)
    if not isinstance(data, dict):
        raise ValueError("quorum config must be an object")
    node_id = data.get("node_id")
    peers = data.get("peers")
    if not isinstance(node_id, str) or not node_id.strip() or not isinstance(peers, list):
        raise ValueError("quorum config requires node_id and peers")
    matches = [p.get("hub_url") for p in peers if isinstance(p, dict) and p.get("id") == node_id]
    if len(matches) != 1 or not isinstance(matches[0], str):
        raise ValueError("quorum config requires one local peer public URL")
    url = matches[0].strip()
    parsed = urlsplit(url)
    if parsed.scheme != "https" or not parsed.hostname or any(c.isspace() for c in url) or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
        raise ValueError("local peer public URL must be an HTTPS origin")
    if parsed.port is not None and not 1 <= parsed.port <= 65535:
        raise ValueError("local peer public URL must be an HTTPS origin")
    return url.rstrip("/")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("expected a quorum config path")
        print(local_public_url(Path(sys.argv[1])))
    except (OSError, ValueError, TypeError, json.JSONDecodeError):
        print("invalid or inaccessible private quorum configuration", file=sys.stderr)
        sys.exit(1)
