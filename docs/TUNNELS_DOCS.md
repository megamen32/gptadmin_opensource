# Tunnels

The hub needs to be reachable from the internet (so your AI / MCP clients can
connect). You don't need your own domain — GPT‑Админ supports two auto-tunnels.

## FRP (default)

[FRP](https://github.com/fatedier/frp) is a fast reverse proxy. GPT‑Админ runs
a public FRP server; the installer can auto-register with it.

### Setup

During `gptadmin setup`, choose option **1** (auto-tunnel via FRP). The installer:
1. Downloads the FRP client
2. Registers a random subdomain on the public FRP server
3. Starts the FRP client as a service alongside the hub
4. Prints your public URL: `https://random-sub.frp.bezrabotnyi.com`

### Pros / cons

- ✅ No domain needed, no DNS setup
- ✅ Fast (direct TCP tunnel)
- ⚠️ URL is on `frp.bezrabotnyi.com` (shared domain)
- ⚠️ Free FRP server has rate limits

## Cloudflare Tunnel

[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
creates a secure outbound tunnel to Cloudflare's edge. You need a Cloudflare
account and a domain on Cloudflare.

### Setup

During `gptadmin setup`, choose the Cloudflare option. Or configure later:

```bash
gptadmin tunnel cloudflare
```

You'll need:
- `CLOUDFLARE_TOKEN` — a Cloudflare API token with Tunnel permissions
- A domain managed by Cloudflare

The CLI:
1. Installs `cloudflared`
2. Creates a tunnel
3. Binds it to a subdomain on your Cloudflare domain
4. Starts `cloudflared` as a service
5. Prints your public URL: `https://hub.yourdomain.com`

### Pros / cons

- ✅ Your own domain
- ✅ Cloudflare's DDoS protection + edge caching
- ✅ No inbound ports needed on your server
- ⚠️ Requires a Cloudflare account + domain

## Your own domain (nginx + Certbot)

If you already have a server with a public IP and a domain:

1. Point a DNS A record to your server
2. Use the provided nginx config template: `deploy/nginx/` (copy and edit)
3. Get a certificate: `certbot --nginx -d hub.yourdomain.com`
4. Run the hub on localhost, nginx proxies to it

```bash
# Example nginx location block
location / {
    proxy_pass http://127.0.0.1:25900;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";  # for MCP SSE
}
```

## Which to choose?

| Use case | Recommended |
|----------|-------------|
| Quick start, no domain | FRP (auto-tunnel) |
| Own domain, want DDoS protection | Cloudflare Tunnel |
| Already have a server + domain | nginx + Certbot |
| Local dev only | none (use `localhost:25900`) |

## See also

- [Getting Started](./GETTING_STARTED.md)
- [Configuration](./CONFIGURATION.md) — `PUBLIC_ORIGIN` etc.
- [Hub](./HUB.md)
