# GrepMesh live readiness and Ollama Bridge audit

Role: Lead
Class: Full
Status: read-only audit in progress
Created: 2026-08-10

## Original request

Проверить, действительно ли GrepMesh добавлен и работает в живой системе,
закончена ли реализация, сравнить скорость с обычным поиском и найти, где
установлены Ollama, Ollama Bridge и их конфигурация.

## Objective

Получить evidence-backed отчёт по текущему локальному host-100: source/build
состоянию GrepMesh, live service/listener/MCP surface, Ollama installation and
configuration, Bridge installation/configuration, and a bounded search latency
comparison against ordinary `rg` where a safe local canary is available.

## Business canary

Назвать точные пути, systemd units, процессы, listeners, MCP endpoint/tool
результат и конфигурационные источники Ollama/Bridge; не объявлять production
готовым только по unit tests или source files.

## Confirmed scope and exclusions

- Read-only inspection and temporary benchmark data only.
- No install, restart, deploy, config mutation, secret output, or public bind.
- Do not print tokens, API keys, private URLs with credentials, or private key
  material.

## Initial estimate

25 / 50 / 90 active minutes.

## Evidence log

- 2026-08-10 13:41 MSK: host-100 (`admin-server-100`) has no `ollama`/`ollama-bridge` binary in PATH, no `ollama.service` or `grepmesh-mcp.service`, and no listeners on `11434` or `9419`. GrepMesh exists only as source/build output under `/home/admin/gptadmin/grepmesh`; debug binary is present. `/home/admin/.codex/config.toml` has MCP entries but no `grepmesh`/`9419` entry. No local production enrollment was found.
- 2026-08-10 13:46 MSK: read-only SSH audit reached `server01` and `admin-server-88`; Mac SSH target `whitetransport-mac-mini-2012` resolved to `mac-mini-2012.lan` and had no Ollama binary/process/listener. `server01` runs `/usr/local/bin/ollama` 0.32.1 as `ollama.service` (`/etc/systemd/system/ollama.service`, drop-in `ollama.service.d/90-runtime-admin.conf`) on `127.0.0.1:11434`, with live `/api/version` and `/api/tags`.
- 2026-08-10 13:49 MSK: `server01` bridge path is not a literal `ollama-bridge` unit. `ollama-request-watchdog-proxy.service` runs `/usr/local/sbin/ollama-model-queue-proxy` on `127.0.0.1:11437` and targets `http://127.0.0.1:11434`; `litellm.service` runs `/opt/litellm/venv/bin/litellm --config /opt/litellm/config.yaml --host 0.0.0.0 --port 4000`. `/opt/litellm/config.yaml` and `/opt/llm-stack-deploy/litellm/config.yaml` map local Ollama models through `127.0.0.1:11437`; LiteLLM health is live on port 4000. `ollama-runtime-agent.service` is a separate settings agent on `127.0.0.1:11436`. Secret-bearing environment values were not recorded.
- 2026-08-10 13:53 MSK: `server-88` runs `/usr/local/bin/ollama` 0.31.1 via `/etc/systemd/system/ollama.service` on `*:11434`; `qwen-embedding-ollama-gpu.service` and `qwen-embedding-ollama-cpu.service` run separate Ollama instances on `127.0.0.1:11438`/`11439`; active `qwen-embedding-gateway.service` (`/opt/qwen-embedding-gateway/releases/bc24e5a93d3a/gateway.py`) listens on `203.0.113.10:11440` and brokers those embedding runtimes. `litellm.service` is installed but disabled and no port 4000 listener was present.
- 2026-08-10 13:57 MSK: controlled temporary benchmark used 1,000 small text files and 7 warm repetitions after separating corpus from benchmark output so watcher events did not invalidate the result. Raw `rg -n -F` median was 0.010 s; GrepMesh local MCP HTTP `tools/call search_text` median was 1.110 s (mean 1.081 s, max 1.120 s). This is approximately 111x slower for small local search in the current prototype; no five-host live GrepMesh latency was measured because no production GrepMesh listener is installed.
- 2026-08-10 14:00 MSK: source/build proof remains green from the preceding implementation pass (`cargo clippy --all-targets -- -D warnings`, Rust all-target tests, Go `go test ./...`, and independent MCP Inspector canary), but live audit proves source/build completion is not the same as Codex registration or production deployment.
- 2026-08-10 14:05 MSK: `/home/admin/.codex/config.toml` contains no GrepMesh/9419 entry. Hermes’ active default is `MiniMax-M3` via `minimax-oauth`; the Ollama Cloud lines in `/home/admin/.hermes/.env` are commented examples, not an active local daemon configuration. `/home/admin/.hermes/memos-plugin/config.yaml` points embedding and host fallback to `http://203.0.113.10:4000/v1`; host-100 reached server01 LiteLLM health with HTTP 200. Temporary benchmark processes/directories were cleaned and no 9419/19419 listener remained.

## Result

Audit complete: source/build milestone is complete, but live production installation, Codex registration, five-host enrollment, and performance readiness are not complete.
