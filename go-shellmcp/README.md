# go-shellmcp

Primary GPTAdmin `rootd` / `shellmcp` transport.

The old Python `client/rootd.py` / `client/shellmcp.py` transport is deprecated and kept only as a compatibility fallback for old/source installs. New deployments should use `go-shellmcp` / `rootd-go-canary`.

Implemented in this prototype:

- `/version`
- `/system/info`
- `/system/health`
- `/exec`
- `/exec/live` NDJSON streaming
- background jobs via `{"background": true}` + `GET /jobs/<job_id>`
- stdout/stderr spooled to disk with bounded tail in RAM
- timeout + process-group kill on Linux/macOS
- token auth compatibility bootstrap
- optional signed long-poll queue runner
- durable queue result outbox under `SHELL_OUTBOX_DIR`
- `SHELL_MODE=long_poll|webhook` heartbeat mode
- optional signed heartbeat to GPTAdmin hub
- `/file?path=...` for authenticated spool file retrieval

Compatibility notes:

- Python `client/rootd.py` / `client/shellmcp.py` is deprecated.
- Keep the `shellmcp.service` service name for compatibility; current production overrides it to execute the Go binary.

Run locally:

```bash
cd go-shellmcp
SHELL_PORT=25990 SHELL_TOKEN=test go run ./cmd/rootd-go
curl -H 'Authorization: Bearer test' http://127.0.0.1:25990/system/health
curl -H 'Authorization: Bearer test' -H 'Content-Type: application/json' \
  -d '{"cmd":"printf hello"}' http://127.0.0.1:25990/exec

curl -H 'Authorization: Bearer test' -H 'Content-Type: application/json' \
  -d '{"cmd":"echo live"}' http://127.0.0.1:25990/exec/live
```

## Tests

```bash
go test ./...
./scripts/cross-build.sh
go test -tags=stress ./internal/server -run TestStressExecHTTP -count=1
REQUESTS=120 WORKERS=20 ./scripts/stress-local.sh
```

`cross-build.sh` verifies Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 builds.
`stress-local.sh` starts a local rootd-go process, runs concurrent `/exec` requests, checks background jobs, and verifies large stdout spill files.

## Default execution user

When `SHELL_DEFAULT_USER` is set and a request does not explicitly set `run_as_user`/`user`, rootd-go runs commands that do not mention `sudo` as that user:

```bash
SHELL_DEFAULT_USER=admin
SHELL_DEFAULT_HOME=/home/admin
SHELL_DEFAULT_CWD=/home/admin
```

Commands containing a `sudo` token stay in the service/root context, so privileged operations can still be requested explicitly with `sudo ...`.
