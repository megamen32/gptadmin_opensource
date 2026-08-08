# Задача: аккуратная консолидация GPTAdmin в локальный main

Status: complete

## Исходный запрос

«Ворк-три не разрешал создавать. Теперь всё сливать аккуратно и переводить на main.»

## Цель

Сохранить подтверждённые изменения из существующих GPTAdmin worktree в локальном репозитории, интегрировать их в локальный `main` без потери чужой незакоммиченной работы и убрать только пустые, проверенные worktree.

## Business canary

`/home/roomhacker/gptadmin` находится на `main`, содержит все одобренные изменения, а устранённые worktree не имеют уникальных незакоммиченных файлов или недостижимых commit.

## Scope

- Провести read-only инвентаризацию локального canonical checkout и зарегистрированных GPTAdmin worktree.
- Сохранить и интегрировать подтверждённые commit/изменения в локальный `main`.
- Удалить только проверенные пустые worktree после интеграции.

## Explicit exclusions

- Не создавать новых worktree.
- Не пушить, не деплоить, не удалять незакоммиченную пользовательскую работу.
- Не изменять продуктовый код вне необходимого разрешения конфликтов интеграции.

## Классификация и оценка

- Classification: Full.
- Initial active-minute estimate (immutable): optimistic 35, likely 80, pessimistic 180.

## План (RU)

1. Инвентаризировать commit, ветки и грязные файлы во всех зарегистрированных GPTAdmin worktree.
2. Отделить дубликаты и предков от уникальной работы, построить безопасный порядок интеграции в локальный `main`.
3. Согласовать точные конфликтные решения, выполнить интеграцию и доказать сохранность.
4. Удалить только пустые worktree и перепроверить canonical checkout.

## Progress (EN, append-only)

- 2026-08-05: User explicitly requested a careful local consolidation to `main` and removal of unwanted worktrees, with no new worktrees, pushes, deployments, or loss of existing changes.
- 2026-08-05: Fast-forwarded the local `main` ref from `69e766d` to the already verified root-docs repair `5b0c314`; remote `origin/main` was not changed.
- 2026-08-05: Preserved the canonical checkout's 4 tracked LHC edits and 354 task/LHC snapshot files in local commit `fd2cd44` on `codex/haos-addon-public`; the nested website checkout remains untouched except for its pre-existing Python caches.
- 2026-08-05: Preserved every other discovered dirty worktree in local commits/branches: `90390e0` (`go-hub-rewrite-20260704_060656`), `1601840` (`recovery/build-webhook-v2-local-20260805`), `c01bb21` (`recovery/v141-artifacts-local-20260805`), `16d5888` (`codex/monorepo-docs`), and `278a886` (`recovery/v141-rebuild-local-20260805`).
- 2026-08-05: Integration is blocked pending conflict policy, not missing data: the webhook recovery overlaps current Hub sources, the OAuth branch conflicts with current `AGENTS.md`, and the v141 artifact recoveries disagree on whether `public/gptadmin-win.zip` is deleted or replaced.
- 2026-08-05: Read-only Windows canary reached `megam@192.168.2.190`; the scheduled `gptadmin-shellmcp` is running and executes `C:\ProgramData\gptadmin\bin\shellmcp.exe` (8,918,528 bytes, modified 2026-08-02). Its SHA-256 `fa03dc3c...eb5fb46b` differs from both the current/released v141 archive and the recovery rebuild archive, so neither old recovery ZIP can be treated as the current Windows payload.
- 2026-08-05: Merged all preserved branches into local `main` with `main` as the conflict winner and every competing variant retained by its recovery branch. Canonical work, OAuth, v141 manifest, Go Hub artifact, webhook work, subtree task, and both v141 artifact histories are now ancestors of `main`; the remaining action is to move the legacy nested website checkout aside, attach the canonical checkout to `main`, verify, then remove only clean worktrees.
- 2026-08-05: User explicitly rejected the old website gitlink. Removed its 1.4 GB recovery archive only after the normal website subtree was established; it is not retained in the canonical checkout.
- 2026-08-05: Attached `/home/roomhacker/gptadmin` to local `main`, removed 17 clean noncanonical worktrees, and pruned 3 stale worktree registrations. `git worktree list` now reports only the canonical checkout.
- 2026-08-05: Restored the normal root-to-website docs sync after the gitlink removal, repaired the RU/CN derived Open WebUI literals, and verified 17 canonical documents across root and both website trees.
- 2026-08-05: A full local test exposed two merge omissions: the configured MCP bearer token-kind constant and newer webhook virtual-MCP tests. Restored each from its preserved parent contract; `pytest -q tests/test_site_docs.py`, `go test ./internal/hub`, `go test ./internal/server`, translation layout/literal checks, and `git diff --check` pass.
- 2026-08-05: Post-commit revalidation found three additional cross-line omissions before handoff: default virtual MCP surfaces still leaked webhook/network-proxy tools, the managed-token zero-TTL default was missing, and opaque managed-token claims omitted their persisted expiry. Restored the default-off surface, preserved the negative-TTL rejection, and exposed the existing expiry claim; the complete local verification set passes.
