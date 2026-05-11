# GPTAdmin

<p align="center">
  <strong>Connect ChatGPT to your own servers.</strong><br>
  A small self-hosted control plane that lets an AI assistant inspect machines, run commands, read logs, fix configs, restart services, and report what changed.
</p>

<p align="center">
  <a href="#why">Why</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#chatgpt-action-setup">ChatGPT Action setup</a> ·
  <a href="#security-model">Security</a>
</p>

---

## What is GPTAdmin?

GPTAdmin is an open-source server-control plugin for ChatGPT-style assistants.

It gives your assistant a narrow HTTP API for real administration tasks: list your machines, inspect system state, run shell commands, read service logs, edit files through controlled operations, and return a useful report instead of just telling you what to copy into SSH.

The public landing page describes the idea as an assistant “that does, not only advises”: install software, validate configs, restart services, check logs, and automate routine maintenance across your own machines. See the live demo/docs at <https://became.bezrabotnyi.com>.

> GPTAdmin is best understood as **SSH-like power exposed through a self-hosted API**. Treat it with the same seriousness as root SSH access.

---

## Why

Modern assistants are good at DevOps reasoning, but most of the time they still stop at instructions:

```text
Run this command.
Paste the output.
Now edit this file.
Restart that service.
Send me the logs.
```

GPTAdmin closes that loop. You can ask:

```text
Check why nginx is down on server-100 and fix it if the config error is obvious.
```

The assistant can then:

1. list registered servers;
2. read system/service state;
3. inspect `journalctl` or nginx logs;
4. propose or apply a small fix;
5. restart the service;
6. verify the result;
7. summarize exactly what happened.

This repository is intentionally small and hackable. It is not trying to be a full enterprise dashboard. It is a practical building block for homelabs, personal servers, small teams, internal bots, and experiments with AI-assisted operations.

---

## Use cases

GPTAdmin is useful when you want an assistant to help with routine server work:

| Area | Examples |
| --- | --- |
| Linux administration | systemd status/restart, package installation, firewall checks, sshd/fail2ban diagnostics |
| Logs and debugging | `journalctl`, nginx, PostgreSQL, app logs, crash loops, failed units |
| Web infrastructure | nginx config validation, Certbot checks, Docker Compose health, reverse proxy debugging |
| VPN and networking | WireGuard/OpenVPN setup, port checks, routing/firewall inspection |
| Databases | PostgreSQL/Redis diagnostics, backups, slow queries, vacuum/maintenance checks |
| Game and hobby servers | Minecraft server setup, backups, service restart, resource checks |
| Fleet convenience | one ChatGPT Action that can work with multiple registered machines |

---

## How it works

GPTAdmin has two main services:

```text
ChatGPT / assistant / script
        |
        | Bearer CTL_TOKEN
        v
+-------------------+
| GPTAdmin hub_proxy |
+-------------------+
        |
        | Bearer ROOTD_TOKEN
        v
+-------------------+
| rootd agent        |
+-------------------+
        |
        v
local shell / SSH backend / OS tools
```

### Components

- **`hub_proxy`** — the central HTTP API. It receives assistant requests, knows which agents are alive, and proxies calls to the selected server.
- **`rootd`** — the agent running on a target machine. It performs local operations and returns structured results.
- **`rootd_pure`** — a dependency-light agent variant using only the Python standard library, useful for minimal Unix-like systems.
- **OpenAPI schema** — the contract imported into ChatGPT Actions or other tool-calling systems.
- **CLI installer** — an interactive setup flow for installing services and managing tokens.

Agents register themselves through heartbeats. The hub keeps live registrations in memory. Assistant requests usually include `?server=<name>`, and the hub forwards the operation to the matching agent.

---

## Repository layout

```text
cli/                         interactive installer and service manager
deploy/                      Linux/Windows install helpers
services/main_package/
  hub_proxy.py               central hub / reverse proxy
  client/rootd.py            FastAPI server agent
  client/rootd_pure.py       stdlib-only agent for minimal systems
  client/rootd_linux.py      Linux command backend
  client/rootd_win.py        Windows command backend
  client/rootd_ssh.py        SSH command backend
docs/                        architecture, ChatGPT setup, API, security, operations
public/                      OpenAPI schema and plugin/action metadata examples
tests/                       smoke/integration tests
tools/                       build and audit helpers
examples/                    environment examples
```

---

## Quick start

### 1. Install dependencies

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

### 2. Start the hub and one local agent

```bash
export CTL_TOKEN="dev-control-token-change-me"
export ROOTD_TOKEN="dev-agent-token-change-me"
export HUB_URL="http://127.0.0.1:48653"
export ROOTD_URL="http://127.0.0.1:48652"

uvicorn services.main_package.hub_proxy:app --host 127.0.0.1 --port 48653 &
uvicorn services.main_package.client.rootd:app --host 127.0.0.1 --port 48652 &
```

### 3. ChatGPT Action setup

GPTAdmin works well as a custom ChatGPT Action because the hub exposes an OpenAPI schema.

1. Open the GPT editor.
2. Create a new Action.
3. Import the OpenAPI schema from your hub or from `public/openapi.json`.
4. Replace the schema `servers.url` with your own hub URL.
5. Set authentication to API key / Bearer token.
6. Use your `CTL_TOKEN` as the token.
7. Ask the assistant to call `listServers` first, then use a specific server name for operations.

Recommended assistant instruction:

```text
You are connected to GPTAdmin, a self-hosted server administration API.
First call listServers when the target server is unclear.
Prefer read-only diagnostics before modifying the system.
Before destructive commands, explain the exact command and ask for confirmation.
After every change, verify the result and summarize what changed.
```

More details: [`docs/CHATGPT_ACTION.md`](docs/CHATGPT_ACTION.md).

---

## One-command installer

For a real machine, use the interactive installer:

```bash
curl -sSL https://became.bezrabotnyi.com/install.sh | sudo bash
```

The installer can set up:

- hub + local rootd on the main machine;
- rootd-only agents on additional machines;
- tokens used by the hub and agents;
- systemd services;
- an optional public tunnel when you do not have a static IP or domain.

After installation it prints the hub URL and control token that you can use in ChatGPT Actions.

For local development from this repository, you can also run:

```bash
sudo python cli/gptadmin.py setup
sudo gptadmin status
sudo gptadmin logs hub
sudo gptadmin tokens
```


## API examples

List registered servers:

```bash
curl -sS "$HUB_URL/servers" \
  -H "Authorization: Bearer $CTL_TOKEN"
```

Run a command:

```bash
curl -sS "$HUB_URL/srv/exec?server=server-100" \
  -H "Authorization: Bearer $CTL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"cmd":"systemctl status nginx --no-pager", "timeout": 20}'
```

Get system info:

```bash
curl -sS "$HUB_URL/srv/system/info?server=server-100" \
  -H "Authorization: Bearer $CTL_TOKEN"
```

See [`docs/API.md`](docs/API.md) and [`public/openapi.json`](public/openapi.json).

---

## Security model

GPTAdmin can execute commands on machines where the agent runs. That is powerful and dangerous by design.

Minimum safe setup:

- use long random tokens;
- expose only the hub, not every agent;
- put the hub behind HTTPS;
- bind agents to private networks where possible;
- restrict allowed commands for untrusted users;
- log every operation;
- rotate tokens if configs, logs, terminal output, or backups leak;
- do not connect it to machines you do not own or administer.

Recommended operating pattern:

1. diagnostics first;
2. exact proposed fix second;
3. explicit confirmation for destructive operations;
4. verification after each change;
5. final report with commands and files touched.

Read [`SECURITY.md`](SECURITY.md) before exposing GPTAdmin outside localhost.

---

## Configuration

Copy the example environment file:

```bash
cp examples/gptadmin.env.example .env
```

Important variables:

| Variable | Used by | Description |
| --- | --- | --- |
| `CTL_TOKEN` | hub | Control-plane bearer token. Required. |
| `ROOTD_TOKEN` | agent | Agent bearer token. Required. |
| `HUB_URL` | agent | Hub URL used for heartbeats. |
| `ROOTD_URL` | agent | Public URL of the agent. |
| `ROOTD_PORT` | agent | Agent HTTP port. Default: `48652`. |
| `HUB_PORT` | hub | Hub HTTP port. Default: `48653`. |
| `SSH_HOST` | agent | Enables SSH backend instead of local execution. |
| `SSH_PORT` | agent | SSH port for remote backend. |
| `SSH_USER` | agent | SSH user for remote backend. |
| `SSH_PASSWORD` | agent | SSH password, if password auth is used. |
| `SSH_KEY` | agent | Path to SSH private key. |

---

## SSH backend

If `rootd` should execute commands on another machine instead of the local host, set `SSH_HOST` and optionally `SSH_PORT`, `SSH_USER`, `SSH_PASSWORD`, or `SSH_KEY` before starting the service.

```bash
export SSH_HOST="192.168.1.50"
export SSH_USER="root"
export SSH_KEY="/root/.ssh/id_ed25519"
export ROOTD_TOKEN="agent-token"

python services/rootd.py
```

This is useful when the agent is deployed as a gateway into a private network.

---

## Tests

```bash
pytest -q
```

The tests start a hub and an agent with local development tokens and verify heartbeat, queue, and command execution flows.

---

## Project status

GPTAdmin is an early, practical open-source project extracted from real server-automation work.

Stable enough to experiment with:

- local hub/agent execution;
- multi-server registration;
- command execution through hub;
- OpenAPI import into ChatGPT Actions;
- basic tests and installer scripts.

Still worth improving:

- persistent server registry;
- stricter command policy engine;
- richer audit log;
- web dashboard;
- packaged releases;
- better Windows support;
- approval workflow for dangerous actions.

Contributions are welcome. See [`CONTRIBUTING.md`](CONTRIBUTING.md).

---

## Similar idea, different shape

GPTAdmin is not a replacement for Ansible, Kubernetes, SSH, or observability platforms. It sits in a different layer: a thin action bridge between an AI assistant and machines you control.

Use mature tools for infrastructure state. Use GPTAdmin when you want a conversational operator that can inspect, execute, verify, and summarize.

---

## License

`Creative Commons Attribution-NonCommercial 4.0 International`

This software is free for personal use. You are allowed to modify and distribute it, but you are strictly prohibited from selling this software or charging for any services directly based on it. See [`LICENSE`](LICENSE).
