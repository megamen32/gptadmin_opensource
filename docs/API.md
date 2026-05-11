# API

The hub exposes a small OpenAPI-compatible surface. The assistant talks to the hub, and the hub proxies selected calls to a `rootd` agent.

## Authentication

Every hub request must include the control token:

```http
Authorization: Bearer <CTL_TOKEN>
```

The hub uses the corresponding agent token internally when forwarding requests to `rootd`.

## List servers

```http
GET /servers
```

Returns currently registered agents.

```bash
curl -sS "$HUB_URL/servers" \
  -H "Authorization: Bearer $CTL_TOKEN"
```

## Execute command

```http
POST /srv/exec?server=<server-name>
```

Example:

```bash
curl -sS "$HUB_URL/srv/exec?server=server-100" \
  -H "Authorization: Bearer $CTL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"cmd":"systemctl status nginx --no-pager", "timeout": 20}'
```

Typical response fields:

- `ok` — whether the command succeeded;
- `returncode` — process exit code;
- `stdout` — command stdout;
- `stderr` — command stderr;
- `duration` — execution time when available.

## System information

```http
GET /srv/system/info?server=<server-name>
```

Returns basic system metadata for the selected server.

## OpenAPI schema

See [`../public/openapi.json`](../public/openapi.json). When deploying your own hub, update the schema `servers.url` to your public hub URL before importing it into ChatGPT Actions.
