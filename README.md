> **Deprecated:** the legacy Python ShellMCP/shellmcp transport (`client/shellmcp.py` and `client/shellmcp.py`) is kept only for compatibility. The primary shell transport is now `go-shellmcp` / `shellmcp-go-canary`.

# GPTAdmin Services

This repository contains two small services used to remotely control a machine.

* **services/shellmcp.py** – runs as root and exposes low level operations.
* **services/shellmcp_pure.py** – simplified variant of `shellmcp` that depends only on the
  Python standard library and works on any Unix-like system including macOS.
* **services/hub_proxy.py** – collects heartbeats from multiple `shellmcp` servers and
  proxies requests to them.

## Requirements

```
pip install -r requirements.txt
```

## Installation with ngrok

For an automated setup that installs dependencies, configures systemd units,
and exposes the hub through ngrok, run:

```
./deploy/install_with_ngrok.sh
```

The script prompts for:

* a Bearer token used by both `shellmcp` and `hub_proxy`;
* an ngrok token to publish the proxy.

It downloads the packaged application, creates virtual environment, installs
dependencies, writes systemd units for `shellmcp`, `hub_proxy` and an ngrok
forwarder, then starts all services. The public URL returned by ngrok is
displayed and stored in `ngrok_url.txt` inside the installation directory.

## Running

Start `shellmcp` and `hub_proxy` in separate terminals:

```
SHELLMCP_TOKEN=srv_secret python services/shellmcp.py
CTL_TOKEN=chatgpt_secret python services/hub_proxy.py
```

`shellmcp` can register itself with the hub when `HUB_URL` is set. Each service
accepts tokens through environment variables as shown above.

To run the minimal version that requires no external dependencies use:

```
SHELLMCP_TOKEN=srv_secret python services/shellmcp_pure.py
```

Set `QUEUE_URL` to enable polling mode. In this mode the daemon polls the
queue for tasks and does not start an HTTP server:

```
QUEUE_URL=http://hub:9001/queue SHELLMCP_TOKEN=srv_secret python services/shellmcp_pure.py
```

### SSH backend

If `shellmcp` should execute commands on a remote host instead of locally,
set `SSH_HOST` (and optionally `SSH_PORT`, `SSH_USER`, `SSH_PASSWORD` or
`SSH_KEY`) before starting the service. The server will connect over SSH and
run all commands on that host.


## Hub watchdog

`services/main_package/hub_watchdog.py` is a dependency-free Python watchdog for
`hub_proxy`. It has two modes:

* `--check-once` probes `http://127.0.0.1:9001/version` and runs a restart
  command on failure. Linux deployments use this through
  `gptadmin-hub-watchdog.service` + `gptadmin-hub-watchdog.timer`.
* `--supervise -- <command...>` runs the hub under the watchdog and restarts the
  child when the process exits or the health endpoint fails repeatedly. This mode
  uses only the Python standard library and is suitable for macOS/Windows/manual
  runs.

Linux systemd deployment installs:

```
sudo cp deploy/systemd/gptadmin-hub-watchdog.service /etc/systemd/system/
sudo cp deploy/systemd/gptadmin-hub-watchdog.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now gptadmin-hub-watchdog.timer
```

Cross-platform supervised run example:

```
python services/main_package/hub_watchdog.py --supervise -- \
  python services/main_package/hub_proxy.py
```

Watchdog logs default to `/var/log/gptadmin/hub-watchdog.log`. Configure with
`GPTADMIN_HUB_HEALTH_URL`, `GPTADMIN_HUB_WATCHDOG_INTERVAL`,
`GPTADMIN_HUB_RESTART_COMMAND`, and related environment variables.

## Build & Obfuscation

The repository includes a helper script to create obfuscated, distributable
executables of both services using [PyArmor](https://github.com/dashingsoft/pyarmor).

```
./tools/build.sh
```

Artifacts will be placed in the `build/` directory (packed into
`gptadmin.tar.gz`).  The script performs a small smoke test to ensure the
generated binaries start and respond to basic requests.

### Rotating or revoking tokens

Tokens are stored inside the systemd unit files. To change or revoke a token
edit the relevant unit, update `SHELLMCP_TOKEN` or `CTL_TOKEN` and restart the
service:

```
sudo systemctl edit --full shellmcp.service
sudo systemctl restart shellmcp
```

Repeat for `hub_proxy.service`. Removing a token and restarting effectively
revokes access. Remember to also update any clients that rely on the old token.

## Tests

Basic scripts for manual testing are provided:

```
python services/hub_proxy.py & python services/shellmcp.py &
python tests/test_shellmcp.py
python tests/test_hub.py
```

## public/openapi.yaml

`public/openapi.yaml` documents the API served by `hub_proxy`. It was produced manualy, update it whenever the API changes to refresh the schema.
