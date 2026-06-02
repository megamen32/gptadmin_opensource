#!/usr/bin/env python3
"""Install and manage GPTAdmin generic stdio MCP relay instances.

This is a thin supervisor/config layer around generic_stdio_mcp_relay.py.
It deliberately does not implement a new MCP runtime.

Backends:
  - Linux: systemd service
  - macOS: launchd LaunchDaemon
  - Windows: pure Scheduled Task + PowerShell restart loop
"""
from __future__ import annotations

import argparse
import json
import os
import platform
import plistlib
import re
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional

ROOT = Path(__file__).resolve().parent
RELAY = ROOT / "generic_stdio_mcp_relay.py"
WINDOWS_WRAPPER = ROOT / "run_mcp_agent.ps1"


def die(msg: str) -> None:
    raise SystemExit(msg)


def slug(value: str) -> str:
    out = re.sub(r"[^A-Za-z0-9_.-]+", "-", value.strip()).strip("-._")
    return out or "mcp-agent"


def load_config(path: Path) -> Dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8-sig"))
    if not isinstance(data, dict):
        die(f"config must be object: {path}")
    for key in ("agent_id", "command"):
        if not data.get(key):
            die(f"missing required field {key!r} in {path}")
    args = data.get("args", [])
    if not isinstance(args, list):
        die("args must be a list")
    env = data.get("env", {})
    if not isinstance(env, dict):
        die("env must be an object")
    return data


def system() -> str:
    s = platform.system().lower()
    if s == "darwin":
        return "darwin"
    if s == "windows":
        return "windows"
    return "linux"


def cfg_name(cfg: Dict[str, Any]) -> str:
    return slug(str(cfg["agent_id"]))


def hub(cfg: Dict[str, Any]) -> str:
    return str(cfg.get("hub_url") or cfg.get("hub") or "https://gptadminmcp.bezrabotnyi.com")


def relay_cmd_list(cfg: Dict[str, Any], python: Optional[str] = None, config_path: Optional[Path] = None) -> List[str]:
    py = python or str(cfg.get("python") or sys.executable or "python3")
    if config_path is not None:
        return [py, str(RELAY), "--agent-config", str(config_path)]
    cmd = [py, str(RELAY), "--hub", hub(cfg), "--agent-id", str(cfg["agent_id"])]
    if cfg.get("name"):
        cmd += ["--name", str(cfg["name"])]
    if cfg.get("stdio_format"):
        cmd += ["--stdio-format", str(cfg["stdio_format"])]
    if cfg.get("init_timeout"):
        cmd += ["--init-timeout", str(cfg["init_timeout"])]
    if cfg.get("verbose"):
        cmd += ["--verbose"]
    if cfg.get("token"):
        cmd += ["--token", str(cfg["token"])]
    cmd += [str(cfg["command"]), *[str(x) for x in cfg.get("args", [])]]
    return cmd


def shell_cmd(cfg: Dict[str, Any], python: Optional[str] = None, config_path: Optional[Path] = None) -> str:
    return shlex.join(relay_cmd_list(cfg, python=python, config_path=config_path))


def merged_env(cfg: Dict[str, Any]) -> Dict[str, str]:
    env = {str(k): str(v) for k, v in (cfg.get("env") or {}).items()}
    env.setdefault("PYTHONUNBUFFERED", "1")
    return env


def render_systemd(cfg: Dict[str, Any], cfg_path: Optional[Path] = None) -> str:
    name = cfg_name(cfg)
    user = cfg.get("run_as_user") or cfg.get("user") or "root"
    cwd = str(cfg.get("cwd") or "/")
    env_lines = "\n".join(f"Environment={shlex.quote(k)}={shlex.quote(v)}" for k, v in merged_env(cfg).items())
    cmd = shell_cmd(cfg, python=str(cfg.get("python") or "python3"), config_path=cfg_path)
    return f"""[Unit]
Description=GPTAdmin MCP stdio relay {name}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User={user}
WorkingDirectory={cwd}
{env_lines}
ExecStart=/bin/sh -lc {shlex.quote(cmd)}
Restart=always
RestartSec={int(cfg.get('restart_sec', 5))}
TimeoutStopSec=10
KillMode=mixed
SendSIGKILL=yes
KillSignal=SIGTERM
StartLimitIntervalSec=300
StartLimitBurst=20

[Install]
WantedBy=multi-user.target
"""


def render_launchd(cfg: Dict[str, Any], cfg_path: Optional[Path] = None) -> bytes:
    label = f"com.gptadmin.mcp.{cfg_name(cfg)}"
    py = str(cfg.get("python") or "/usr/bin/python3")
    args = relay_cmd_list(cfg, python=py, config_path=cfg_path)
    env = merged_env(cfg)
    log_dir = str(cfg.get("log_dir") or "/var/log/gptadmin")
    plist = {
        "Label": label,
        "ProgramArguments": args,
        "WorkingDirectory": str(cfg.get("cwd") or "/"),
        "EnvironmentVariables": env,
        "RunAtLoad": True,
        "KeepAlive": True,
        "StandardOutPath": f"{log_dir}/mcp-{cfg_name(cfg)}.out.log",
        "StandardErrorPath": f"{log_dir}/mcp-{cfg_name(cfg)}.err.log",
    }
    if cfg.get("run_as_user"):
        plist["UserName"] = str(cfg["run_as_user"])
    return plistlib.dumps(plist, sort_keys=False)


def render_powershell_wrapper() -> str:
    return r'''param(
  [Parameter(Mandatory=$true)][string]$Config
)
$ErrorActionPreference = "Continue"
$script:LogFile = $null
function Write-Log($Message) {
  $ts = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
  $line = "$ts $Message"
  Write-Output $line
  if ($script:LogFile) { Add-Content -Path $script:LogFile -Value $line -Encoding UTF8 }
}
while ($true) {
  try {
    $cfg = Get-Content -Raw -Path $Config | ConvertFrom-Json
    $logDir = "C:\ProgramData\GPTAdmin\logs"
    if ($cfg.log_dir) { $logDir = [string]$cfg.log_dir }
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null
    $safeName = ([string]$cfg.agent_id) -replace '[^A-Za-z0-9_.-]', '-'
    $script:LogFile = Join-Path $logDir "mcp-$safeName.log"
    $root = Split-Path -Parent $MyInvocation.MyCommand.Path
    $relay = Join-Path $root "generic_stdio_mcp_relay.py"
    if ($cfg.relay_path) { $relay = [string]$cfg.relay_path }
    $python = "python"
    if ($cfg.python) { $python = [string]$cfg.python }
    $args = @($relay, "--agent-config", $Config)
    if ($cfg.cwd) { Set-Location -Path ([string]$cfg.cwd) }
    if ($cfg.env) { $cfg.env.PSObject.Properties | ForEach-Object { [Environment]::SetEnvironmentVariable($_.Name, [string]$_.Value, "Process") } }
    [Environment]::SetEnvironmentVariable("PYTHONUNBUFFERED", "1", "Process")
    Write-Log "starting $python $($args -join ' ')"
    & $python @args *>> $script:LogFile
    Write-Log "relay exited code=$LASTEXITCODE; restarting in 5s"
  } catch {
    Write-Log "wrapper error: $($_.Exception.Message); restarting in 5s"
  }
  Start-Sleep -Seconds 5
}
'''


def render_windows_task_command(cfg_path: Path, cfg: Dict[str, Any]) -> str:
    name = f"GPTAdmin MCP {cfg_name(cfg)}"
    wrapper = str(WINDOWS_WRAPPER)
    tr = f'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "{wrapper}" -Config "{cfg_path}"'
    ru = str(cfg.get("run_as_user") or "SYSTEM")
    if ru.lower() in {"root", "system"}:
        ru = "SYSTEM"
    return f'schtasks /Create /TN "{name}" /SC ONSTART /RU "{ru}" /RL HIGHEST /TR "{tr}" /F'


def render(cfg_path: Path, cfg: Dict[str, Any], backend: str) -> str:
    if backend == "systemd":
        return render_systemd(cfg, cfg_path)
    if backend == "launchd":
        return render_launchd(cfg, cfg_path).decode("utf-8")
    if backend == "windows-task":
        return render_windows_task_command(cfg_path, cfg) + "\n"
    die(f"unknown backend {backend}")


def backend_default() -> str:
    s = system()
    if s == "darwin":
        return "launchd"
    if s == "windows":
        return "windows-task"
    return "systemd"


def run(cmd: List[str]) -> int:
    print("+", shlex.join(cmd))
    return subprocess.call(cmd)


def install(cfg_path: Path, cfg: Dict[str, Any], backend: str) -> None:
    name = cfg_name(cfg)
    if backend == "systemd":
        unit = Path(f"/etc/systemd/system/gptadmin-mcp-{name}.service")
        unit.write_text(render_systemd(cfg, cfg_path.resolve()), encoding="utf-8")
        run(["systemctl", "daemon-reload"])
        run(["systemctl", "enable", "--now", unit.name])
        return
    if backend == "launchd":
        log_dir = Path(str(cfg.get("log_dir") or "/var/log/gptadmin"))
        log_dir.mkdir(parents=True, exist_ok=True)
        plist = Path(f"/Library/LaunchDaemons/com.gptadmin.mcp.{name}.plist")
        plist.write_bytes(render_launchd(cfg, cfg_path.resolve()))
        run(["launchctl", "bootout", "system", str(plist)])
        run(["launchctl", "bootstrap", "system", str(plist)])
        run(["launchctl", "kickstart", "-k", f"system/com.gptadmin.mcp.{name}"])
        return
    if backend == "windows-task":
        WINDOWS_WRAPPER.write_text(render_powershell_wrapper(), encoding="utf-8")
        cmd = render_windows_task_command(cfg_path.resolve(), cfg)
        print(cmd)
        if os.name == "nt":
            subprocess.check_call(cmd, shell=True)
        return
    die(f"unknown backend {backend}")


def status(cfg: Dict[str, Any], backend: str) -> int:
    name = cfg_name(cfg)
    if backend == "systemd":
        return run(["systemctl", "status", f"gptadmin-mcp-{name}.service", "--no-pager"])
    if backend == "launchd":
        return run(["launchctl", "print", f"system/com.gptadmin.mcp.{name}"])
    if backend == "windows-task":
        print(f'schtasks /Query /TN "GPTAdmin MCP {name}" /V /FO LIST')
        return 0
    die(f"unknown backend {backend}")



def uninstall(cfg: Dict[str, Any], backend: str) -> None:
    name = cfg_name(cfg)
    if backend == "systemd":
        unit = f"gptadmin-mcp-{name}.service"
        run(["systemctl", "disable", "--now", unit])
        unit_path = Path(f"/etc/systemd/system/{unit}")
        try:
            unit_path.unlink()
        except FileNotFoundError:
            pass
        run(["systemctl", "daemon-reload"])
        return
    if backend == "launchd":
        label = f"com.gptadmin.mcp.{name}"
        plist = Path(f"/Library/LaunchDaemons/{label}.plist")
        run(["launchctl", "bootout", "system", str(plist)])
        try:
            plist.unlink()
        except FileNotFoundError:
            pass
        return
    if backend == "windows-task":
        task_name = f"GPTAdmin MCP {name}"
        cmd = f'schtasks /Delete /TN "{task_name}" /F'
        print(cmd)
        if os.name == "nt":
            subprocess.call(cmd, shell=True)
        return
    die(f"unknown backend {backend}")

def main() -> int:
    p = argparse.ArgumentParser(description="Manage generic stdio MCP relay instances")
    p.add_argument("action", choices=["validate", "render", "install", "status", "uninstall"])
    p.add_argument("config", type=Path)
    p.add_argument("--backend", choices=["systemd", "launchd", "windows-task"], default=backend_default())
    args = p.parse_args()
    cfg = load_config(args.config)
    if args.action == "validate":
        print(json.dumps({"ok": True, "backend": args.backend, "agent_id": cfg["agent_id"], "service_name": cfg_name(cfg)}, indent=2))
        return 0
    if args.action == "render":
        sys.stdout.write(render(args.config, cfg, args.backend))
        return 0
    if args.action == "install":
        install(args.config, cfg, args.backend)
        return 0
    if args.action == "status":
        return status(cfg, args.backend)
    if args.action == "uninstall":
        uninstall(cfg, args.backend)
        return 0
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
