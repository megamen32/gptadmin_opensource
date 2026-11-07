# GPTAdmin resource baseline: isolated built1104

**Date:** 2026-11-07  
**Scope:** one disposable copy of the real GPTAdmin node package, run under a Docker cgroup capped at 1 GiB RAM and 2 CPUs. No production service restart/deploy, host resource quota, code/build change, commit or push was made.

## Artifact and setup

The tested package was the pre-existing `gptadmin-node-linux-amd64.tar.gz` from `/home/admin/.codex/worktrees/e1a2/gptadmin/.tmp/node-packages`. Its SHA-256 is `cd69ea3fe30ad83ec16f96e46e596bbc1ab88b6188b0c4cd31e33310a8e9f394`; the embedded manifest says version **1104**, source commit `e791f763564db0bf6b2640949033c6d6cb2d751f`, and `source_dirty=false`.

The container was `gptadmin-systemd-acceptance:24.04`; it ran the extracted package binary with a disposable empty config, loopback-only published port `19011`, and test-only auth values. The mounted package was read-only. Applied cgroup readback was `memory.max=1073741824`, `memory.swap.max=0`, `cpu.max=200000 100000`. The earlier `GOMEMLIMIT` setting was not used as a substitute for the kernel memory cgroup.

The runtime reported `/version` as build1104 with the same commit. The container reached its requested stop without OOM; its final state is in `evidence/resources/container-final-state.json`.

## Measurements

| Scenario | Observed result |
|---|---|
| Startup RSS sample | 24,404 KiB |
| 4 parallel harmless `hub.demo` calls through real `exec` | 4/4 completed; Hub-reported elapsed 10 ms; outer HTTP request 1.148366 s; response 1,722 bytes |
| Process RSS sample after parallel calls | 44,236 KiB |
| Targeted schema selection (`hub.demo`) | 479 HTTP bytes |
| `expand` of the selected schema | 288 data bytes (473 HTTP bytes), exact schema returned |
| Full 30-tool Hub schema catalog | 19,247 HTTP bytes |
| Process RSS sample after expand | 43,944 KiB |
| Container memory current / cgroup peak after expand | 61,145,088 / 110,198,784 bytes |
| Container CPU usage, before the parallel phase → after selection/expand | 841,928 → 2,494,839 µs; delta 1,652,911 µs |
| CPU throttling / OOM | 0 throttled periods / no OOM |

Targeted schema selection returned about **2.49%** of the full catalog's HTTP byte count (479 vs 19,247). `expand` then returned the exact selected `demo` schema. These are real responses from built1104, not mocks.

The CPU counter is container-wide: the Hub, its local polling sidecar, and container activity are included. The samples bracketed the parallel request plus the schema and expand calls. RSS values are point samples, not a sampled process RSS maximum; `memory.peak` is the cgroup peak and can include memory outside the process RSS.

## Reproduction and evidence

[`evidence/resources/repro.md`](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/resources/repro.md) records the Docker invocation, cgroup readback commands, bounded JS body, MCP schema/expand calls and cleanup. Key raw receipts are `manifest-1104.json`, `parallel-retry.json`, `schema.json`, `expand.json`, `catalog.json`, `before-parallel.txt`, `after-parallel.txt`, `after-expand.txt`, and `runtime.log`; [`metrics.json`](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/resources/metrics.json) summarizes the numbers.

Two setup details are preserved in `preflight.txt`: the first container start exited because the test omitted required `SHELL_TOKEN`; after adding a disposable value, the same binary started. The first `exec` request correctly returned `instructions_required` with `calls=0`; repeating with its returned context completed the four calls. That ephemeral context was removed from the stored receipt.

## Limits of this baseline

The largest real response measured here was the 19,247-byte tool catalog. The built-in safe `demo` operation returns a small result, so this run does **not** prove memory or latency behavior for a multi-megabyte business result, sustained parallel load, disk-heavy work, remote child MCPs, or production clients. The observed 1 GiB limit is specific to this temporary container; it says nothing about a production service or host-wide quota. No `GOMEMLIMIT`-only result is claimed.

## Последний прогон: четыре одновременных HTTP `exec`

По запросу владельца тот же пакет built1104 был ещё раз запущен в отдельном контейнере с теми же проверенными cgroup limits: RAM 1 GiB, swap 0, CPU 2. Порт Docker назначил динамически и привязал только к loopback. Перед нагрузкой один `exec` сначала получил ожидаемый `instructions_required` (`calls=0`), затем с выданным для того же тестового bearer context успешно вернул warm marker. После прогрева отправлены **четыре независимых одновременных HTTP POST** на `/mcp`, каждый выполнял только 200 ms JS busy loop и возвращал уникальный marker; shell и вложенные MCP вызовы не использовались.

- Выполнено: 4 из 4; все маркеры совпали, ошибок и admission отказов не было.
- Время JS: 200, 200, 200, 204 ms.
- HTTP latency: 224, 244, 257, 265 ms; среднее 248 ms; каждый ответ 184 B.
- RSS процесса Hub перед/после: 87,692 / 100,940 KiB. Максимум sampled RSS PID 1 — 100,940 KiB за 1,218 отсчётов в окне 17:11:21–17:11:48 UTC.
- Cgroup memory current/peak: 119,373,824 / 122,245,120 B. `memory.events`: `max=0`, `oom=0`, `oom_kill=0`; container `OOMKilled=false`.
- Cgroup CPU вырос с 2,368,123 до 6,159,307 µs; два throttled periods, 20,480 µs throttled. CPU delta охватывает весь RSS-sampler интервал, Hub и polling sidecar, поэтому это контейнерное потребление за весь интервал, а не стоимость одних четырёх запросов.
- Очистка: `docker stop -t 10` дошёл до SIGKILL (exit 137), но cgroup OOM-счётчики остались нулевыми; контейнер удалён, loopback port освобождён.

Сырые конкурентные ответы и тайминги: `evidence/resources/concurrent-[1-4].json`, `concurrent-[1-4].time`, `concurrent-summary.json`, `concurrent-rss-samples.tsv`, `concurrent-rss-summary.txt`, `concurrent-before.txt`, `concurrent-after.txt`, `concurrent-runtime.log` и `concurrent-container-final-state.json`. Запуск воспроизводится тем же Docker способом из [`repro.md`](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/resources/repro.md), заменив `-p 127.0.0.1:19011:19011` на `-p 127.0.0.1::19011`; порт читается из `docker inspect`.
