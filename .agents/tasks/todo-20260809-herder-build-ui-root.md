# Unselected defect: Agent Herder package build uses incomplete concurrent UI tree

- Symptom: `npm test -- --run tests/http-api.test.ts` in
  `/home/admin/agents-projects/agent-herder` fails before tests because
  the current dirty `vite.config.ts` resolves `src/web-ui/index.html` to a
  missing `/main.tsx`.
- Smallest evidence: Vite reports `Failed to resolve /main.tsx`; `npx tsc`
  succeeds and `npx vitest run --root . tests/http-api.test.ts` passes 5 tests.
- Scope: unrelated concurrent UI/package work; not changed by this task.
- Blocker: needs its owning UI migration to settle before the package-level
  `npm test` command can be used as release evidence.
