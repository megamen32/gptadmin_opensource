# P0 GPTADMIN Resources Read Receipt

Status: complete
Class: Short / P0 recovery

## Original User Request

> Annotation 1: «это ставь как цель и доделай» — для отсутствующего live
> protocol-level `resources/read` receipt по `ui://widget/admin-v3.html`.
>
> Нормальный — рекомендую

## Objective

Add a safe read-only GPTADMIN plugin capability that returns a live receipt for
an authenticated protocol-level resource read, then use it to prove and close
P0.2 without exposing widget contents or credentials.

## Business Canary

The user-selected GPTADMIN plugin calls the new bounded receipt capability for
`ui://widget/admin-v3.html` and receives: exact URI, exact MIME type, byte size,
SHA-256 digest, and successful authenticated read status. The widget UI remains
renderable, existing Hub tools remain compatible, and no resource text is
returned by the receipt tool.

## Confirmed Scope

- Add one read-only Apps/MCP tool to the public GPTADMIN Hub contract.
- Reuse the existing authenticated `appsSDKResourceRead` path internally.
- Return metadata only: URI, MIME, bytes, SHA-256, and bounded content count.
- Add a failing regression first, then focused/full tests.
- Deploy through the existing reviewed release path and verify with the actual
  user-selected GPTADMIN plugin.
- Update P0.2 only after live receipt and independent Overseer acceptance.

## Explicit Exclusions

- No widget HTML, credential, cookie, token, or authorization value in receipt.
- No OAuth redesign, database/schema/data changes, browser-profile changes, or
  unrelated Fleet/registry cleanup.
- No claim based only on source tests or UI-render text.

## Initial Active-Minute Estimate (immutable)

- Optimistic: 30 minutes
- Likely: 60 minutes
- Pessimistic: 120 minutes

## Estimate Revisions (append-only)

- None.

## Selected Design (RU)

- Новый read-only tool `resource_receipt` принимает только `uri`.
- Он вызывает тот же authenticated resource-read handler, но возвращает только
  `uri`, `mime_type`, `byte_size`, `sha256`, `content_count` и `ok`.
- Для неизвестного/ошибочного ресурса возвращается typed error без содержимого.
- Existing `ui` tool, output template and widget content remain unchanged.

## Selected-plan WSFF

Call-stack tree:

```text
tools/call resource_receipt(uri)
└── appsSDKCall / appsSDKCallForRequest
    └── appsSDKResourceReceipt(request, uri)
        └── appsSDKResourceRead(request, uri)
            └── validate canonical MCP content item
                └── return metadata-only SHA-256 receipt
```

File-tree diff:

```text
go-hub/internal/hub/
├── access_policy.go # server-side read-only authorization allowlist
├── server.go        # descriptor, dispatch, metadata-only receipt helper
└── server_test.go   # authenticated red regressions and no-content assertions
ROADMAP.md          # current P0/Fleet priority wording only
```

Key method signatures:

```go
func (s *Server) appsSDKResourceReceipt(r *http.Request, uri string) map[string]any
func (s *Server) appsSDKResourceRead(r *http.Request, uri string) map[string]any
```

## Progress (EN, append-only)

- 2026-08-02: P0 recovery task opened from the explicit annotation. No code,
  runtime, OAuth, release, or roadmap mutation has started.
- 2026-08-02: Official OpenAI Plugins reference confirms that a data-only tool
  should declare its exact `outputSchema` and read-only annotations, while only
  the render tool carries `_meta.ui.resourceUri` / `openai/outputTemplate`.
  Existing graph and source inspection locate the canonical read at
  `appsSDKResourceRead`; `server.go` and `server_test.go` remain unmodified.
- 2026-08-02: Mandatory pre-implementation Overseer audit launched with the raw
  user context, both active task records, shared-worktree diff, and evidence
  locations. Implementation is waiting on its verdict.
- 2026-08-02: Overseer first returned `STOP_SCOPE_DRIFT`; L confirmed that
  Notify/Fleet are separate requested plans and imposed a hard Fleet freeze
  until the receipt canary. Overseer re-audited and approved receipt-only work.
- 2026-08-02: TDD red 1 failed because `resource_receipt` was absent from the
  Apps tool contract. The direct read-only tool, metadata-only helper, exact
  schema, typed failure, and no-content assertions were added; focused test
  turned green.
- 2026-08-02: TDD red 2 reproduced the original live-session topology:
  `execute(target=hub, tool=resource_receipt)` returned `unsupported hub tool`.
  A read-only Hub alias and pre-lock dispatch made that bridge green.
- 2026-08-02: Reviewer then found schema drift and an unproved read-only token
  path. Shared schema helpers removed drift. A new `gptadmin.read`-only test
  failed with `read-only client cannot call this tool`; the two server-side
  read-only policy allowlists were corrected and the focused test turned green.
- 2026-08-02: Final local gates are green: `go test ./...` (34.581s),
  `go vet ./...`, `go test -race ./...` (37.191s), and scoped
  `git diff --check`. One-time Critic passed the narrow pre-release path while
  explicitly reserving P0.2 closure for the live selected-plugin canary.
- 2026-08-02: Final Reviewer returned `APPROVE` after request-context parity was
  covered with a no-`PublicOrigin` red/green test. Release commit
  `2fcc6bd178de0fb5dc8f371d1c90cfeee42f3ba9` contains exactly `VERSION=140`
  plus the three reviewed Go files and was pushed explicitly to `origin/main`.
  Annotated `v140^{}` resolves to that exact commit; tag-dispatched release run
  `30730908343` is in progress. The exact-commit gate's only two failures were
  duplicate isolated-HOME OpenCode wrapper failures (`opencode-real` absent);
  303 tests passed, 6 skipped, all changed/relevant Go tests plus remaining
  ShellMCP, ProxyRelay, UI, CLI version, and auto-update gates passed.
- 2026-08-02: Public release `v140` completed successfully in GitHub Actions run
  `30730908343`; all Windows, macOS, Linux/Docker, UI, installer, manifest,
  SBOM, mirror, and vulnerability-scan jobs passed. The public Linux amd64 Hub
  binary identifies itself as build `140`, commit
  `2fcc6bd178de0fb5dc8f371d1c90cfeee42f3ba9`.
- 2026-08-02: The all-in-one updater briefly installed v140 but its unrelated
  legacy watchdog wiring failed, so the prepared rollback guard restored v134.
  L then used the narrower reviewed binary-only deployment: primary local
  `/version` and the public origin both report exact build `140` and commit
  `2fcc6bd178de0fb5dc8f371d1c90cfeee42f3ba9`; the v134 backup remains under
  `/var/backups/gptadmin/resource-receipt-v140-20260802T064730/`.
- 2026-08-02: HAOS accepted the existing short-lived HMAC-signed primary reclaim
  on the first attempt and logged `reclaimed_primary`; the primary FRP routes
  then became public. The HAOS local add-on's generic `update` endpoint rejected
  same-version package `1.0.6`, so it was immediately restarted on v134 and then
  rebuilt with Supervisor's supported `ha apps rebuild --force` command. Its
  direct `/version` now also reports exact build `140` and the same commit, while
  watchdog logs remain `primary_ok` with the standby demoted.
- 2026-08-02: The actual installed GPTADMIN plugin returned the live canonical
  server-side resource receipt for `ui://widget/admin-v3.html`: `ok=true`, MIME
  `text/html;profile=mcp-app`, `byte_size=8481`, `content_count=1`, and SHA-256
  `b48187f079796e786c205ca1d8fef997f779024a565f60abaec9504a2a71073f`.
  The receipt returned no widget content. A separate live `gptadmin_ui` call
  rendered the interactive dashboard successfully. Public OAuth authorization
  and protected-resource metadata endpoints both return HTTP 200 JSON.
- 2026-08-02: Mandatory final Overseer independently returned `APPROVE` and
  authorized P0.2 closure with the bounded wording: a live authenticated plugin
  proved a server-side canonical resource-read receipt for the widget URI plus
  a separate live UI render; this is not a captured literal ChatGPT transcript.
  `ROADMAP.md` now marks P0 complete, queues S21 Android debug/remote-control as
  the next separate goal, and leaves the selected Normal Fleet cleanup queued.
