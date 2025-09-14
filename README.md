# GPTAdmin Services

This repository contains two small services used to remotely control a machine.

* **rootd.py** – runs as root and exposes low level operations.
* **hub_proxy.py** – collects heartbeats from multiple `rootd` servers and
  proxies requests to them.

## Requirements

```
pip install -r requirements.txt
```

## Installation with ngrok

For an automated setup that installs dependencies, configures systemd units,
and exposes the hub through ngrok, run:

```
./install_with_ngrok.sh
```

The script prompts for:

* a Bearer token used by both `rootd` and `hub_proxy`;
* an ngrok token to publish the proxy.

It downloads the packaged application, creates virtual environment, installs
dependencies, writes systemd units for `rootd`, `hub_proxy` and an ngrok
forwarder, then starts all services. The public URL returned by ngrok is
displayed and stored in `ngrok_url.txt` inside the installation directory.

## Running

Start `rootd` and `hub_proxy` in separate terminals:

```
ROOTD_TOKEN=srv_secret python rootd.py
CTL_TOKEN=chatgpt_secret python hub_proxy.py
```

`rootd` can register itself with the hub when `HUB_URL` is set. Each service
accepts tokens through environment variables as shown above.

### SSH backend

If `rootd` should execute commands on a remote host instead of locally,
set `SSH_HOST` (and optionally `SSH_PORT`, `SSH_USER`, `SSH_PASSWORD` or
`SSH_KEY`) before starting the service. The server will connect over SSH and
run all commands on that host.

## Build & Obfuscation

The repository includes a helper script to create obfuscated, distributable
executables of both services using [PyArmor](https://github.com/dashingsoft/pyarmor).

```
./build.sh
```

Artifacts will be placed in the `build/` directory (packed into
`gptadmin.tar.gz`).  The script performs a small smoke test to ensure the
generated binaries start and respond to basic requests.

### Rotating or revoking tokens

Tokens are stored inside the systemd unit files. To change or revoke a token
edit the relevant unit, update `ROOTD_TOKEN` or `CTL_TOKEN` and restart the
service:

```
sudo systemctl edit --full rootd.service
sudo systemctl restart rootd
```

Repeat for `hub_proxy.service`. Removing a token and restarting effectively
revokes access. Remember to also update any clients that rely on the old token.

## Tests

Basic scripts for manual testing are provided:

```
python hub_proxy.py & python rootd.py &
python test_rootd.py
python test_hub.py
```

## openapi.json

`openapi.json` documents the API served by `hub_proxy`. It was produced manualy, update it whenever the API changes to refresh the schema.

