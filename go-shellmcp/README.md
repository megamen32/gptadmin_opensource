# go-shellmcp

Experimental Go rewrite of GPTAdmin `rootd` / `shellmcp` transport.

Current goal: keep the production Python agent untouched while building a small, memory-stable Go core for command execution and HTTP transport.

Implemented in this prototype:

- `/version`
- `/system/info`
- `/system/health`
- `/exec`
- bounded stdout/stderr in RAM; tail is kept, not full unbounded output
- timeout + process-group kill on Linux/macOS
- token auth compatibility bootstrap

Not implemented yet:

- signed GPTAdmin hub auth
- heartbeat/register
- long-poll queue transport
- durable callback outbox
- MCP stdio adapter
- auto-update

Run locally:

```bash
cd go-shellmcp
SHELL_PORT=25990 SHELL_TOKEN=test go run ./cmd/rootd-go
curl -H 'Authorization: Bearer test' http://127.0.0.1:25990/system/health
curl -H 'Authorization: Bearer test' -H 'Content-Type: application/json' \
  -d '{"cmd":"printf hello"}' http://127.0.0.1:25990/exec
```
