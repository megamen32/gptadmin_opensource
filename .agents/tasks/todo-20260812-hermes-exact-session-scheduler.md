# Hermes exact-session scheduler ingress

Status: todo — research route in progress; no implementation or runtime mutation authorized.

## Lifecycle and runtime identity

- Started at: 2026-08-12 Europe/Moscow (UTC+3), exact wall-clock time unknown (legacy harness start).
- Lifecycle provenance: Created by Lead from the user-attached architecture request at `/home/admin/.codex/attachments/4648c193-621d-43b4-bebb-fa0e2db02739/pasted-text.txt`; this is the immutable todo snapshot.
- Last task-file mtime observed: not observed before creation; creation is the first write.
- Minimum / maximum active minutes: 75 / 150 (immutable initial range; research alone is capped at 20 minutes).
- Harness: Codex desktop, shared primary checkout `/home/admin/gptadmin`.
- PID: unknown (harness-managed).
- Agent session: current Codex task, exact identifier unavailable to the repository.
- PID status: unknown (harness-managed).
- Last PID signal: task created; no child dispatched yet.
- Last task-file transition: none → `todo-20260812-hermes-exact-session-scheduler.md`.

## User request (source and retained summary)

The attached request diagnoses why cron, heartbeat, the existing megamen32 adapter, and prior core PRs do not safely continue an exact Hermes conversation. Its selected direction is: **a plugin/adaptor owns durable scheduling; Hermes core owns exact session identity, fresh authorization, queueing, and delivery.**

Requested outcome: deliver the recommended two-part product increment, with a narrow upstream-compatible `exact-session ingress` (target is `(session_key, session_id)`, compression lineage may continue, `/new`, `/reset`, `/resume`, route loss and a successor from real user input fail closed) and a separate local durable plugin scheduler backed by SQLite. The requested first regression is A scheduled, `/new` produces B, and the event must not enter B. The full original request is retained at the absolute attachment path above (889 lines; its SHA/content must be treated as the request source).

## Confirmed outcome and business canary

Outcome: a user can request “continue this analysis in two hours”; after restart, the due event is admitted only when the original physical transcript (or compression-only successor) still owns its logical route, then becomes an ordinary queued Hermes turn in the original Telegram thread. A timer captured before `/new` must return an explicit stale rejection and must never enter the replacement transcript.

Business canary: create a one-shot timer in a real authenticated Hermes Telegram/topic session A; exercise a safe test `/new` to create B; run the due delivery and verify B receives no scheduled turn and the scheduler has a durable stale receipt. Separately, an unchanged or compression-only lineage must deliver one ordinary turn into the original topic/transcript after a controlled worker restart. This is required before any production claim; source tests are only precursor evidence.

## Owned scope

- Research the current installed Hermes/plugin seam and the managed local Hermes adapter without modifying either.
- Specify a minimal ingress surface equivalent to `inject_message(content, session_key, expected_session_id)` with host-owned target provenance and synchronous acceptance/rejection.
- Specify the separate plugin scheduler: schedule/list/cancel/reschedule, SQLite outbox, restart recovery, retry receipts, and a bridge that does not expose session identifiers to the model.
- Plan tests for route-CAS, compression-only lineage, stale replacement, authorization, and scheduler recovery.

## Explicit exclusions and stop conditions

- Do not patch Hermes core, create a local Hermes fork, modify cron, heartbeat, tool registry, platform-specific Telegram/Discord code, prompt builder, timer schema, native timer commands, assistant/system injection, steer/interrupt, or runtime services in research.
- Do not restart, deploy, send Telegram messages, schedule real work, alter persistent Hermes state, create/switch/merge/delete branches or worktrees, or commit foreign changes without a direct user authorization at that exact action.
- Hermes source ownership remains plugin/configuration only unless a plugin-only canary fails and the user explicitly selects a core-source-policy change. An upstream PR design is allowed; a local core fork is not.
- Stop and request a decision if the installed runtime has no documented plugin/configuration seam for target provenance and exact ingress, or if the selected path requires an unapproved source-policy change.

## Research worker assignment

Role/mode: Worker / research.

Goal: establish the smallest supported plugin/configuration path and the exact evidence needed to decide whether the requested ingress can be delivered without local Hermes core drift.

Allowed read-only paths: `/home/admin/gptadmin/.last-human-commit/adapters/hermes/`, `/home/admin/gptadmin/.last-human-commit/`, documented installed Hermes configuration/plugin paths, and relevant tests/docs. Excluded: all mutations, all runtime control, all network effects, unrelated repository paths.

Acceptance: append to this same card (not a separate handoff) a compact evidence table with: actual adapter entrypoint and plugin seams; how current session key/id provenance is exposed; exact route/lineage APIs; whether a supported core ingress exists; candidate test locations; and a bounded <=20-minute execution graph. Cite path and symbol for every material claim. Mark `NEEDS_RETHINK` if the source-ownership boundary blocks the desired behavior.

Return format: append detailed evidence here, then return only a TL;DR to Lead. Maximum active minutes: 20. Do not edit source files; other agents may be active, so do not revert or overwrite their changes.

## Lead notes

- Classification: research first; likely Full if the research confirms more than 30 active minutes and a material core/plugin boundary decision.
- Current checkout warning already issued to user: `/home/admin/gptadmin` is on `agent/gptadmin-parallel-browser-flows-scoped`; `main` and `origin/main` exist, but no branch operation is authorized.
- Existing working tree is dirty (158 paths observed; 65 outside `.agents/`). Preserve all foreign work and stage only this task's snapshots when/if a checkpoint commit is safe.

## Worker stop note — superseded target (2026-08-12)

- User clarified that the target is the Hermes core/upstream exact-session ingress, not only the GPTAdmin/local adapter seam.
- No repository source, installed runtime, adapter implementation, or plugin seam was explored in this worker turn; therefore no technical evidence is added and no claim about supportability is made.
- No source files, runtime state, branches, worktrees, or task lifecycle snapshots were modified by this worker. Further exploration is intentionally stopped pending Lead re-routing/redecomposition for the Hermes core/upstream target.
