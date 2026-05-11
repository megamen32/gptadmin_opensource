# Operations

## Useful commands

```bash
gptadmin status
gptadmin logs hub
gptadmin logs rootd
gptadmin tokens
gptadmin uninstall
```

## systemd

Typical services:

- `gptadmin-hub` or `hub_proxy`;
- `gptadmin-rootd` or `rootd`;
- optional tunnel service.

Check status:

```bash
sudo systemctl status gptadmin-hub --no-pager || sudo systemctl status hub_proxy --no-pager
sudo systemctl status gptadmin-rootd --no-pager || sudo systemctl status rootd --no-pager
```

Follow logs:

```bash
sudo journalctl -u gptadmin-hub -f || sudo journalctl -u hub_proxy -f
sudo journalctl -u gptadmin-rootd -f || sudo journalctl -u rootd -f
```

## Production notes

- Put the hub behind HTTPS.
- Do not expose agents directly.
- Store tokens in root-readable environment files.
- Back up service configuration before changing ports or tokens.
- Prefer private networking, VPN, or firewall rules between hub and agents.
- Start with read-only diagnostics when testing a new assistant profile.

## Updating OpenAPI

`public/openapi.json` is the schema imported by ChatGPT Actions. Update it when endpoints change, especially operation IDs, request bodies, response schemas, and the `servers.url` value.
