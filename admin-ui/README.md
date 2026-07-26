# GPTAdmin Admin UI

Static React + TypeScript + Vite foundation for the operator console. The
release artifact is the Vite `dist/` directory; Node is only required for
development and CI.

## Local commands

```bash
npm install
npm test
npm run lint
npm run build
```

## Profile instructions contract

The first screen reads and writes `/admin/api/instruction-sets/default` using
same-origin browser credentials:

- `GET` returns `{ "id", "content", "version", "updated_at" }` and an `ETag`
  header.
- `PUT` sends `{ "content": "..." }` with the in-memory `If-Match` ETag.
- `412 Precondition Failed` is rendered as a stale-edit conflict and never
  overwrites the local draft.
- Content is limited to 16 KiB by UTF-8 byte length in the client. The Hub
  remains authoritative and must enforce the same limit server-side.

Authentication is delegated to the existing same-origin admin session. This
UI does not persist credentials or tokens in browser storage.
