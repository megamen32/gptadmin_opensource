# Current Roadmap Priorities Review

Status: complete
Class: Direct

## Original User Request

> какие у нас сейчас roadmap и приоритеты в ней?

## Objective

Give the user a current, evidence-backed ordered summary of the repository roadmap and distinguish active/selected priorities from proposed work.

## Business Canary

The final Russian answer names the current top priority, lists the remaining ordered roadmap items, identifies `Proposed` work separately, and links the canonical repository evidence.

## Confirmed Scope

- Read the canonical `ROADMAP.md`.
- Cross-check active task records for drift or newer blockers.
- Use the existing project graph as a secondary project-content index.
- Report current priorities without changing product scope.

## Explicit Exclusions

- No roadmap reprioritization or implementation.
- No deployment, production mutation, or live infrastructure checks.
- No security, secrets, permissions, database, observability, or provider audit.

## Initial Active-Minute Estimate (immutable)

- Optimistic: 6 minutes
- Likely: 10 minutes
- Pessimistic: 15 minutes

## Estimate Revisions (append-only)

- 2026-08-02: likely 14 minutes / pessimistic 20 minutes. Trigger: a concurrent
  `ROADMAP.md` edit landed during the audit, requiring a fresh read and a second
  Overseer gate.

## Evidence

- `ROADMAP.md:3-10` declares top-first ordering and leaves only P0.2 unchecked:
  authenticated `resources/read` must render `ui://widget/admin-v3.html`.
- `ROADMAP.md:12-16` contains only a placeholder M1, not a selected deliverable.
- Current `ROADMAP.md:18-23` contains a completed S21 proxy item and one open
  Notify-controlled agent-jobs proposal awaiting user architecture selection.
- Reauthorization, HAOS recovery/agent-resume, and public v139 release task
  records each contain a `Complete` evidence entry, but no task record proves
  the exact P0.2 widget-render canary.
- `work-20260802-notify-webhook-agent-orchestration.md` is the untracked Full
  planning record behind that proposal; implementation has not started.
- The first literal Russian `graphify query` returned no matching nodes. The
  required vocabulary-expanded retry traversed code-oriented task/auth/resource
  nodes but no roadmap nodes, so canonical priority claims remain file-based.
- Overseer re-audited the concurrent roadmap update and returned `APPROVE`:
  P0.2 remains pending, M1 remains a placeholder, and both `Proposed` entries
  must be reported separately.

## Result

Current roadmap priorities are fully summarized with canonical evidence and
explicit separation between open P0, placeholder M1, completed supporting work,
and unselected `Proposed` work. No roadmap reprioritization was performed.

## Self-Improve Fallback Record

The shared `.agents/last-human-commit/self-improve.md` was freshly modified by
another task and is hands-off, so the mandatory compact record is persisted here.

## 2026-08-02 — roadmap priorities review (Direct)

- What slowed or confused L? A raw Russian graph query found no nodes until exact graph-vocabulary expansion; `ROADMAP.md` then changed concurrently at 04:32.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? Proposed: `ctx_execute_file` needs an explicit stable project-root/cwd parameter.
- What operation or error repeated? Two file reads resolved against wrong roots (`agents-projects`, `whitetransport`); guard: use cwd-bound execution or a fixed-size direct read.
- State: needs human decision
