# Cloud OS — Connectable Local Computer for Cloud Agents

## Overview

Cloud OS transforms any local computer (Mac, PC, Linux) into a connectable resource for cloud-based AI agents (OpenCode, Codex, Pi). The agent remains in the cloud with its skills, MCP servers, and session state, while the browser extension + native host expose the local machine's filesystem, processes, and network as a remote API.

```
Cloud Agent Runtime
  Codex / OpenCode / Pi
          │
   Remote Computer API
  (go-hub/internal/cloudos)
          │
   go-proxyrelay (gpr1 tickets)
          │
   WebSocket relay │ 8787
          │
 Browser Extension + Native Host
  (Chrome native messaging)
          │
   ┌──────┼─────────────┐
 files  processes     network
  (fs     (exec)    (NetworkProxyController
  streams) streams)  + SOCKS5/HTTP CONNECT)
                       │
                 LAN + Internet
```

## Integration with Existing Infrastructure

Cloud OS **reuses** the existing GPTAdmin proxy/tunnel infrastructure instead of reimplementing it:

| Existing Component | Role in Cloud OS |
---|---|
| **go-proxyrelay** | WebSocket stream relay for all Cloud OS streams (control, filesystem, process, network). HMAC-SHA256 `gpr1.` ticket auth, frame protocol, rate limiting, revocation. |
| **go-hub/internal/hub/network_proxy.go** | Capability lifecycle for **network** streams. Request → Approve → IssueStreamGrants → Open → Revoke. Signs gpr1 tickets with relay key. |
| **go-shellmcp/internal/networkproxy/** | Edge data plane for **network** streams. Policy enforcement, dialer, stream bridging, local SOCKS5/HTTP proxy. |
| **tunnels/** | Hub ingress tunnels (Cloudflare/ngrok/FRP). Separate from Cloud OS, makes hub reachable. |

### What's NEW in Cloud OS

| Component | What it adds |
---|---|
| `go-hub/internal/cloudos/` | Computer registry, pairing, heartbeat, filesystem/process stream management, API routes |
| `browser-os/` | macOS-style desktop UI (Files, Terminal, OpenCode, Computers panel) |
| `browser-extension/` | Chrome MV3 extension — pairing, heartbeat, ticket delivery to native host |
| `native-host/` | Go binary — real OS operations via Chrome native messaging |

### Stream Types

| Stream Type | Transport | Auth | New/Existing |
---|---|---|---|
| **control** | proxyrelay WebSocket | gpr1 ticket (Cloud OS signs) | **New** — computer registration, heartbeat |
| **filesystem** | proxyrelay WebSocket | gpr1 ticket (Cloud OS signs) | **New** — file list/read/write |
| **process** | proxyrelay WebSocket | gpr1 ticket (Cloud OS signs) | **New** — process list/exec/kill |
| **network** | proxyrelay WebSocket | gpr1 ticket (NetworkProxyController signs) | **Existing** — full capability lifecycle |

## Architecture

### 1. Browser OS (`browser-os/`)

A macOS-style desktop environment running in the browser as a Next.js application. Provides:

- **Desktop** with wallpaper, icons, and window management
- **Dock** with app launcher (Files, Terminal, OpenCode, Computers)
- **Files app** — file browser showing Cloud Home, Workspace, and connected Computers as volumes
- **Terminal app** — command-line interface with `[Cloud] $` and `[MacBook Home] $` contexts
- **Computers panel** — list of connected computers with pairing and disconnect controls
- **OpenCode window** — code editor with cloud session integration
- **Menu bar** with Cloud/Local Computer/Network status indicators

Tech: Next.js 16, React 19, TypeScript, Tailwind CSS 4, Zustand

### 2. Remote Computer API (`go-hub/internal/cloudos/`)

Go package providing the REST API. All routes are under `/api/v1/cloud-os/`.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/cloud-os/computers` | GET | List all connected computers |
| `/api/v1/cloud-os/computers/pair` | POST | Pair a new computer (returns computer ID + control ticket) |
| `/api/v1/cloud-os/computers/{id}` | GET | Get computer status |
| `/api/v1/cloud-os/computers/{id}/heartbeat` | POST | Update last-seen timestamp |
| `/api/v1/cloud-os/computers/{id}/ticket` | POST | Issue agent-side relay ticket for a stream type |
| `/api/v1/cloud-os/computers/{id}/disconnect` | POST | Revoke computer, close all streams, post revocation to relay |
| `/api/v1/cloud-os/files/list` | POST | List directory contents |
| `/api/v1/cloud-os/files/read` | POST | Read file content |
| `/api/v1/cloud-os/files/write` | POST | Write file content |
| `/api/v1/cloud-os/processes/list` | POST | List running processes |
| `/api/v1/cloud-os/processes/exec` | POST | Execute a command |
| `/api/v1/cloud-os/processes/kill` | POST | Kill a process |
| `/api/v1/cloud-os/network/connect` | POST | TCP/HTTP/SOCKS5 proxy (delegates to NetworkProxyController) |

Components:
- `types.go` — Data types (Computer, ComputerOperation, FileEntry, ProcessEntry, ExecResult)
- `registry.go` — Thread-safe in-memory computer registry + ID/JTI generators
- `handler.go` — 13 HTTP handlers (pair, heartbeat, ticket, list, status, disconnect, files, processes, network)
- `router.go` — Route registration for go-hub's ServeMux
- `tunnel.go` — TunnelManager interface, ProxyRelayTunnelManager (wraps proxyrelay + NetworkProxyController), NoopTunnelManager fallback
- `registry_test.go` — Tests including concurrent access safety

### 3. Tunnel Manager (`tunnel.go`)

The `ProxyRelayTunnelManager` is the production tunnel that wraps existing infrastructure:

```go
// Creation
signer, _ := ticket.NewSigner(relayKey)           // from go-proxyrelay/internal/ticket
client := proxyrelay.NewClient(relayURL)            // WebSocket client
mgr := cloudos.NewProxyRelayTunnelManager(
    cloudos.DefaultProxyRelayConfig(),
    signer,    // RelayTicketSigner interface
    client,    // RelayClient interface
    registry,
)
```

For **network** streams, the handler returns a pointer to the existing
`/proxy-control/v1` endpoints so the caller uses `NetworkProxyController`
for the full request → approve → issue → open → revoke lifecycle.

For **control/filesystem/process** streams, Cloud OS signs its own
gpr1 tickets using the same relay key. The browser extension obtains
agent-side tickets via `/api/v1/cloud-os/computers/{id}/ticket` and
passes them to the native host via native messaging.

### 4. Browser Extension (`browser-extension/`)

Chrome MV3 extension:

- **Service worker** (`background.js`) — native host lifecycle, pairing via `/api/v1/cloud-os/computers/pair`, heartbeat via `/api/v1/cloud-os/computers/{id}/heartbeat`, ticket issuance, relay.connect to native host
- **Popup** (`popup.html` + `popup.js`) — status display, connect/disconnect controls
- **Content script** (`content.js`) — bridges Browser OS page to extension, exposes `window.cloudOS` API
- **Native host manifest** (`native-host-manifest.json`) — registers the native helper

Pairing flow (updated):
1. User clicks "Enable this Computer"
2. Extension connects to native host, gets system info
3. Extension POSTs to `/api/v1/cloud-os/computers/pair` with name, OS, capabilities
4. Hub registers computer, signs gpr1 control ticket, returns computer ID + ticket + relay URL
5. Extension passes control ticket to native host via native messaging
6. Native host connects to proxyrelay as "agent" peer with the ticket
7. Heartbeat (every 30s) via `/api/v1/cloud-os/computers/{id}/heartbeat`
8. For filesystem/process operations, extension requests agent tickets via `/api/v1/cloud-os/computers/{id}/ticket`

### 5. Native Host (`native-host/`)

Go binary for Chrome native messaging:

- **Protocol** (`protocol.go`) — 4-byte LE length-prefixed JSON
- **Filesystem** (`filesystem.go`) — list, read, write; path validation against home dir
- **Processes** (`processes.go`) — list (ps/tasklist), exec with timeout, kill
- **System** (`system.go`) — hostname, OS, arch, capabilities
- **Security** (`security.go`) — symlink-aware path validation, binary file detection
- **Dispatcher** (`dispatcher.go`) — routes methods to handlers

Cross-platform: Unix (ps, kill) and Windows (tasklist, taskkill).

## Security Model

Default policy — everything is allowed:

| Capability | Default |
|-----------|---------|
| Files read/write | Enabled |
| Processes exec | Enabled |
| Network proxy | Enabled |

Three security domains (inherited from existing infrastructure):

1. **Hub Control Plane** — Ed25519-signed API requests, OAuth, admin password
2. **Relay Data Plane** — HMAC-SHA256 `gpr1.` tickets, separate key, replay protection
3. **Edge Policy Enforcement** — native host validates paths, process execution sandboxing

Additional protections:
- File operations sandboxed to the user's home directory (symlink escape prevention)
- Process execution: configurable timeout (default 30s), 1MB output cap
- File reads capped at 10MB
- Relay enforces: frame size limits, pending queue depth, byte transfer limits, idle/lifetime timeouts, bandwidth throttling
- Immediate revocation via Disconnect (posts gpr1 revoke ticket to relay's `/v1/control/revoke`)
- Single-use tickets with JTI replay cache

## MVP Canary Flow

1. Cloud has OpenCode configured with skills, MCP, and saved session
2. User opens Chrome on local Mac/PC
3. Installs extension and native helper
4. Clicks "Enable this Computer"
5. Extension pairs via `/api/v1/cloud-os/computers/pair`, gets gpr1 control ticket
6. Native host connects to proxyrelay as agent peer
7. `MacBook Home` appears in Browser OS Files
8. OpenCode reads and modifies a local file (filesystem stream via proxyrelay)
9. OpenCode launches a local process and receives stdout (process stream via proxyrelay)
10. OpenCode makes a TCP request to `192.168.1.1` via NetworkProxyController + proxyrelay
11. User closes browser, opens again — same cloud session, same computer handle
12. "Disconnect" posts revocation ticket to relay, immediately aborts all streams

## Running Locally

### Browser OS
```bash
cd browser-os
bun install
bun dev
# Open http://localhost:3000
```

### Native Host
```bash
cd native-host
make build
sudo make install
```

### Browser Extension
1. Open `chrome://extensions/`
2. Enable Developer Mode
3. Click "Load unpacked" -> select `browser-extension/`
4. Register native host manifest (see `browser-extension/README.md`)

### Wiring into go-hub
```go
import (
    "github.com/megamen32/gptadmin/go-hub/internal/cloudos"
    "github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

cloudosReg := cloudos.NewRegistry()

// With relay (production):
signer, _ := ticket.NewSigner(relayKey)
client := proxyrelay.NewClient(relayURL)
tunnelMgr := cloudos.NewProxyRelayTunnelManager(
    cloudos.DefaultProxyRelayConfig(), signer, client, cloudosReg,
)

// Without relay (development):
// tunnelMgr := &cloudos.NoopTunnelManager{}

cloudos.RegisterRoutes(mux, cloudosReg, tunnelMgr)
```

## Next Steps

- [ ] Implement native host proxyrelay WebSocket client (connect as agent peer with gpr1 ticket)
- [ ] Build actual stream data flow: Browser OS -> hub -> proxyrelay -> native host
- [ ] Add file watcher support in native host
- [ ] Implement SOCKS5/HTTP CONNECT proxy in native host (reuse go-shellmcp/internal/proxy)
- [ ] Add offer signing/delivery for network streams via hub's `/proxy-agent/offers`
- [ ] Add OAuth-based computer authentication
- [ ] Build multi-computer session management
- [ ] Generate extension PNG icons from SVG source
- [ ] Add end-to-end integration tests
