#!/usr/bin/env python3
"""Small user-level CloudOS agent: files and a deliberately narrow terminal."""
import argparse
import getpass
import json
import os
import platform
import secrets
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def within(root: Path, requested: str) -> Path:
    candidate = (root / requested.lstrip("/")).resolve()
    if candidate != root and root not in candidate.parents:
        raise ValueError("path outside CloudOS workspace")
    return candidate


class Agent:
    def __init__(self, args):
        self.hub, self.name, self.os_name = args.hub.rstrip("/"), args.name, args.os
        self.root = Path(args.root).expanduser().resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.state = Path(args.state).expanduser()
        self.state.parent.mkdir(parents=True, exist_ok=True)
        previous = {}
        if self.state.exists():
            try:
                previous = json.loads(self.state.read_text())
            except json.JSONDecodeError:
                previous = {"computer_id": self.state.read_text().strip()}
        self.token = args.token or previous.get("token") or secrets.token_urlsafe(32)
        self.computer_id = previous.get("computer_id", "")

    def save_state(self):
        temporary = self.state.with_suffix(self.state.suffix + ".tmp")
        temporary.write_text(json.dumps({"computer_id": self.computer_id, "token": self.token}))
        os.chmod(temporary, 0o600)
        temporary.replace(self.state)

    def pair(self):
        body = json.dumps({"name": self.name, "os": self.os_name,
            "capabilities": ["files", "terminal"], "session_id": platform.node(),
            "endpoint": self.endpoint, "agent_token": self.token}).encode()
        req = urllib.request.Request(self.hub + "/computers/pair", body, {"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as response:
            self.computer_id = json.load(response)["computer"]["id"]
        self.save_state()

    def heartbeat(self):
        try:
            if not self.computer_id:
                self.pair(); return
            req = urllib.request.Request(self.hub + "/computers/" + self.computer_id + "/heartbeat", method="POST")
            with urllib.request.urlopen(req, timeout=10): pass
        except Exception:
            self.pair()

    def files(self, requested):
        path = within(self.root, requested)
        if not path.is_dir(): raise ValueError("not a directory")
        entries = []
        for child in sorted(path.iterdir(), key=lambda p: (not p.is_dir(), p.name.lower())):
            stat = child.stat()
            entries.append({"name": child.name, "type": "directory" if child.is_dir() else "file",
                "size": stat.st_size, "modified": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(stat.st_mtime)),
                "path": "/" + str(child.relative_to(self.root))})
        relative = path.relative_to(self.root)
        return {"path": "/" if relative == Path('.') else "/" + str(relative), "entries": entries}

    def execute(self, command):
        commands = {
            "hostname": ["hostname"],
            "whoami": ["whoami"],
            "pwd": None,
            "ls": None,
            "dir": None,
        }
        if command not in commands: raise ValueError("command is not enabled; allowed: hostname, whoami, pwd, ls, dir")
        if command == "pwd": return {"stdout": str(self.root) + "\n", "stderr": "", "exit_code": 0}
        if command in ("ls", "dir"): return {"stdout": "\n".join(p.name for p in sorted(self.root.iterdir())) + "\n", "stderr": "", "exit_code": 0}
        result = subprocess.run(commands[command], cwd=self.root, capture_output=True, text=True, timeout=10)
        return {"stdout": result.stdout, "stderr": result.stderr, "exit_code": result.returncode}


def handler(agent):
    class Handler(BaseHTTPRequestHandler):
        def reply(self, status, payload):
            data = json.dumps(payload).encode(); self.send_response(status)
            self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(data)))
            self.end_headers(); self.wfile.write(data)
        def authorized(self): return self.headers.get("Authorization") == "Bearer " + agent.token
        def do_GET(self):
            if not self.authorized(): return self.reply(401, {"detail":"unauthorized"})
            try:
                if self.path.startswith("/v1/files"):
                    return self.reply(200, agent.files(urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("path", ["/"])[0]))
                self.reply(404, {"detail":"not found"})
            except (OSError, ValueError) as err: self.reply(400, {"detail":str(err)})
        def do_POST(self):
            if not self.authorized(): return self.reply(401, {"detail":"unauthorized"})
            try:
                if self.path != "/v1/exec": return self.reply(404, {"detail":"not found"})
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
                self.reply(200, agent.execute(body.get("command", "")))
            except (OSError, ValueError, subprocess.SubprocessError) as err: self.reply(400, {"detail":str(err)})
        def log_message(self, *_): pass
    return Handler


def main():
    parser = argparse.ArgumentParser(); parser.add_argument("--hub", required=True); parser.add_argument("--name", required=True)
    parser.add_argument("--os", required=True); parser.add_argument("--listen", required=True); parser.add_argument("--root", required=True)
    parser.add_argument("--state", required=True); parser.add_argument("--token"); args = parser.parse_args()
    agent = Agent(args); agent.endpoint = "http://" + args.listen; agent.heartbeat()
    def heartbeat_loop():
        while True:
            agent.heartbeat()
            time.sleep(20)
    threading.Thread(target=heartbeat_loop, daemon=True).start()
    ThreadingHTTPServer((args.listen.rsplit(":", 1)[0], int(args.listen.rsplit(":", 1)[1])), handler(agent)).serve_forever()

if __name__ == "__main__": main()
