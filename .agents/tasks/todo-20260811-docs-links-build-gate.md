# Documentation links and build gate

Status: todo

## User request

Find why the public documentation site links to the wrong site and every
documentation page returns 404. Add a build-time test that prevents a build
when a documentation link is incorrect or documentation routes return 404.

## Objective and business canary

The deployed documentation link opens the intended public site; every
documentation navigation target returns a non-404 success response. The website
build/CI fails locally before publication if either contract regresses.

## Scope and exclusions

Owned: website documentation routes, link generation/configuration, and a
focused build-time regression gate. Excluded: deployment, DNS/Nginx changes,
unrelated website redesign, and external documentation providers.

## Initial control limit

- Minimum / maximum active minutes: 15 / 35 (immutable initial range)
- Started at: 2026-08-11T22:37:35+03:00
- Lifecycle provenance: created by Lead from explicit user request; no prior task lineage
- Last task-file mtime observed: 2026-08-11T22:37:35+03:00

## Runtime identity

- Harness: Codex desktop
- PID: 1894973
- Agent session: unknown (harness did not expose a stable session id)
- PID status: alive at task creation
- Last PID signal: shell parent PID reported 1894973 at 2026-08-11T22:37:35+03:00
- Last task-file transition: created todo at 2026-08-11T22:37:35+03:00

## Research contract

Read-only trace of the public documentation link, website router/build output,
and existing website test conventions. Identify the smallest fix and add a
focused failing regression test before implementation. Do not deploy or alter
production routing.

Acceptance: name the root cause, the owned files, and the exact build-gate
command that proves internal documentation targets are served rather than 404.
