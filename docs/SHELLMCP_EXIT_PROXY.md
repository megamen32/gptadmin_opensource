# ShellMCP exit HTTP proxy

`networkproxy-exit` runs on the client PC and exposes `127.0.0.1:3126` for
HTTP CONNECT and SOCKS5. Any program can use that address as its proxy. The
Chrome extension is only the node selector; it never receives Hub credentials.

Each selected ShellMCP runs `networkproxy-agent` in pull mode. The Hub issues a
fresh client/agent grant for every CONNECT, queues the signed agent offer, and
the agent opens the remote half of the relay stream.

Required Hub configuration:

- `GPTADMIN_NETWORK_PROXY_RELAY_KEY_FILE`: existing relay HMAC key (32+ bytes)
- `GPTADMIN_NETWORK_PROXY_RELAY_URL`: `wss://` base URL for `proxyrelay`
- `MCP_RELAY_AGENT_TOKEN`: bearer credential supplied to each authorised agent

Agent configuration:

- `OFFERS_URL=https://hub.example/proxy-agent/offers?agent_id=shell:edge-1`
- `OFFER_TOKEN_FILE`: file containing `MCP_RELAY_AGENT_TOKEN`
- `HUB_PUBLIC_KEY_FILE`: write the `public_key` returned from authenticated
  `GET /proxy-agent/public-key` to this file; it is safe to distribute to
  authorised agents.
- `AGENT_ID=shell:edge-1`

Client configuration:

- `HUB_URL=https://hub.example`
- `RELAY_URL=wss://relay.example/stream`
- `CTL_TOKEN_FILE`: Hub control credential, readable only by the companion
- `NODES_FILE`: copied and completed from
  `go-shellmcp/deploy/networkproxy-exit-nodes.json.example`

Install the corresponding systemd template as `networkproxy-exit@<user>` on
the client PC and `networkproxy-agent@<user>` on each exit node. Then load
`chrome-shellmcp-exit-proxy/` as an unpacked extension and select an authorised
node. The extension points Chrome at the same loopback proxy applications use.

This document is setup guidance, not proof of a live deployment. A successful
canary must fetch a unique HTTP endpoint through `127.0.0.1:3126` and observe
the selected exit node at that endpoint.
