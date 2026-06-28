# GPTAdmin Shell MCP Docker E2E

This harness checks Linux installation paths and tunnel wiring in disposable Docker containers.
New scenario names use **shellmcp** terminology. New Linux installs created by this harness assert the user-facing service name
`gptadmin-shellmcp.service` and description `GPTAdmin Shell MCP Agent`.

Run from repo root:

```bash
docker compose -f tests/e2e/docker/docker-compose.yml up --build --abort-on-container-exit --exit-code-from shellmcp-e2e
```

Covered scenarios:

- `user-public-hub-shellmcp`: non-sudo install, public hub URL, polling shellmcp transport.
- `sudo-frp-shellmcp`: sudo/system install, built-in FRP path, shellmcp service files.
- `tunnel-backends-shellmcp`: Cloudflare Quick Tunnel and FRP backends with fake binaries.

The container stubs `systemctl`/`journalctl` because regular Docker containers do not run systemd.
The installer and downloaded artifacts are still executed for real inside Ubuntu.
