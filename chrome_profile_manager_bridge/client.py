#!/usr/bin/env python3
"""Capability is supplied on stdin, never as a command line argument."""
import argparse
import json
import socket
import sys
from host import receive, send, socket_path

parser = argparse.ArgumentParser()
parser.add_argument("method", choices=["list", "get", "enable", "disable", "uninstall", "launch_app"])
parser.add_argument("extension_id", nargs="?")
args = parser.parse_args()
secret = sys.stdin.readline().strip()
with socket.socket(socket.AF_UNIX) as sock:
    sock.settimeout(30)
    sock.connect(socket_path(secret))
    with sock.makefile("rwb") as stream:
        send(stream, {"secret": secret, "method": args.method, "extension_id": args.extension_id})
        response = receive(stream)
        print(json.dumps(response, ensure_ascii=False))
        if "error" in response: sys.exit(1)
