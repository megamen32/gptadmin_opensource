# Tunnel Backends

GPTAdmin hub can be exposed to the internet using various tunnel backends. This allows LLM clients (Qwen, ChatGPT, etc.) to connect to your local hub without requiring a public IP address or domain.

## Quick Start

### Cloudflare Quick Tunnel (Recommended for Testing)

No account required! Just install `cloudflared` and run:

```bash
TUNNEL_TYPE=cloudflare uv run python -m gptadmin.hub
```

This will create a temporary tunnel and print the public URL:
```
======================================================================
Hub public URL: https://random-words-123.trycloudflare.com/mcp
Add this to your LLM client (Qwen, ChatGPT, etc.)
======================================================================
```

**Install cloudflared:**
- **Linux:** `curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared`
- **macOS:** `brew install cloudflared`
- **Windows:** `winget install cloudflare.cloudflared`

**Limitations:**
- URL changes on every restart
- Tunnel only lives while the process is running
- No custom domain support

### ngrok

Alternative tunnel service with a free tier:

```bash
TUNNEL_TYPE=ngrok uv run python -m gptadmin.hub
```

**Install ngrok:**
- **Linux:** See https://ngrok.com/download
- **macOS:** `brew install ngrok`
- **Windows:** `winget install ngrok`

**Optional:** Sign up at https://ngrok.com for a free auth token to remove limitations (longer sessions, custom subdomains).

### FRP (Self-Hosted)

If you have your own VPS with `frps` running, you can use FRP:

```bash
TUNNEL_TYPE=frp \
FRP_SERVER_ADDR=frp.example.com \
FRP_SERVER_PORT=7000 \
FRP_TOKEN=your-secret-token \
FRP_SUBDOMAIN=myhub \
FRP_DOMAIN=example.com \
uv run python -m gptadmin.hub
```

This will expose your hub at `https://myhub.example.com/mcp`.

### No Tunnel (Local Only)

If you don't need external access or have your own reverse proxy:

```bash
uv run python -m gptadmin.hub
```

Or explicitly:

```bash
TUNNEL_TYPE=none uv run python -m gptadmin.hub
```

## Architecture

```
LLM Client (Qwen, ChatGPT)
    ↓ HTTPS
Tunnel Service (Cloudflare/ngrok/FRP)
    ↓ encrypted tunnel
GPTAdmin Hub (local, port 9001)
    ↓ forwards to
GPTAdmin shellmcp (multiple machines)
```

**Hub** is your control plane:
- Manages authentication tokens
- Routes requests to the correct shellmcp
- Scrubs sensitive data from logs
- Maintains registry of connected servers

**Tunnels** just expose hub to the internet. Hub doesn't care which tunnel you use.

## Security Considerations

1. **Hub holds all the keys** — whoever controls hub controls all connected shellmcp instances (with root access). This is why hub should run locally on a machine you trust.

2. **Tunnel services see your traffic** — when using Cloudflare or ngrok, your traffic passes through their servers. However:
   - All traffic is HTTPS (encrypted in transit)
   - Hub scrubs sensitive tokens from logs
   - You can use your own VPS with FRP for full control

3. **Quick tunnels are temporary** — Cloudflare Quick Tunnels generate random URLs that change on restart. This is actually good for security: even if someone discovers your URL, it's useless after you restart.

4. **Production use** — for production, consider:
   - Using FRP with your own VPS (full control)
   - Or Cloudflare with a custom domain (stable URL, but still through CF)
   - Or running hub on a VPS with a public IP (no tunnel needed)

## Implementation Details

Tunnel backends are implemented as pluggable modules in `src/gptadmin/tunnels/`:

- `base.py` — Abstract `TunnelBackend` class and `TunnelInfo` dataclass
- `cloudflare.py` — Cloudflare Quick Tunnel implementation
- `ngrok.py` — ngrok implementation (uses local API at `localhost:4040`)
- `frp.py` — FRP implementation (requires frps server)

Each backend:
1. Checks if the required binary is installed (`is_available()`)
2. Starts the tunnel process (`start()`)
3. Parses the output to get the public URL
4. Returns `TunnelInfo` with the URL and process handle

Hub automatically stops the tunnel on shutdown.

## Adding Custom Tunnel Backends

To add a new tunnel backend:

1. Create `src/gptadmin/tunnels/mytunnel.py`
2. Implement `TunnelBackend` abstract class:
   ```python
   from .base import TunnelBackend, TunnelInfo
   
   class MyTunnel(TunnelBackend):
       @property
       def name(self) -> str:
           return "My Custom Tunnel"
       
       def is_available(self) -> bool:
           # Check if binary exists
           return shutil.which("mytunnel") is not None
       
       def start(self, local_port: int) -> TunnelInfo:
           # Start tunnel process
           # Parse output to get public URL
           return TunnelInfo(
               public_url="https://...",
               backend="mytunnel",
               process=process
           )
   ```
3. Add to `__init__.py` exports
4. Add to hub's `main()` function
