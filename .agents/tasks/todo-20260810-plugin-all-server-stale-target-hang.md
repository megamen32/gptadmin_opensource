# Plugin all-server flow waits on stale targets — 2026-08-10

## Symptom

The real Plugin Flow sends an all-server uptime request, keeps ChatGPT in an active `Отслеживание` / stop state, and does not finish within the bounded acceptance window.

## Evidence

- Screenshot: `trash/logs/plugin-flow-failure-20260810.png`.
- Screenshot shows `16 shell-целей: 8 online, 7 stale и 1 ожидает approval`.
- Screenshot shows commands sent to all discovered targets and the current step stuck at `Проверка времени работы сервера`.
- Public ingress during the attempt returned HTTP 200 for relay calls and downstream results for available targets.

## Smallest confirmed defect

The all-server orchestration path waits on stale or approval-gated targets instead of returning a bounded partial result with explicit per-target states.

## Blocker / next action

Needs a separate behavior fix and regression: bounded per-target timeout/cancellation and a completed response that reports `online`, `stale`, and `awaiting approval` without holding the whole ChatGPT turn open.
