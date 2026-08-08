# Задача: webhook-оркестрация ИИ-агентов из notify

## Исходный запрос

«я хочу сделать чтобы мой notify мог запускать ии-шек… мы можем сделать систему
webhooks которая запускает заданый mcp скажем agent-herder для запуска opencode
который пишет в ответ hi? и удобную страницку для управление этим в вебморде и
правило все что доступно в вебморде должно быть доступно ии через
плагин/mcp/customgptaction чтобы он могли создавать webhook и прокидывать на
нужный сервер для теста на 100 и скажем маку»

Дополнение пользователя: «добавь в [Agent Herder] создание новой сессии, и ещё
`new_or_resume`: если есть именованная сессия `repair_100`, продолжить её, иначе
создать и передать сообщение из цепочки notify → GPTAdmin webhook →
ShellMCP:100 → Agent Herder».

## Цель

Итоговая product-цель — безопасный управляемый путь Notify → подписанный
GPTAdmin webhook → конфигурационно фиксированный ShellMCP host → локальный
Agent Herder `new_or_resume` с durable terminal receipt обратно в Notify.
Agent Herder prerequisite завершён; текущий slice выполняет полный server-100
E2E, затем тот же контракт на Mac, после чего добавляет UI/MCP parity для
управления bounded webhook routes.

## Business canary

Server-100 canary: dedicated Notify producer scope разрешает только job
`repair_100`; один critical disk event создаёт durable delivery, подписанный
HMAC POST с его стабильным delivery key достигает GPTAdmin route, route запускает
только `shell:roomhacker-server-100`, helper читает mode-0600 profile и вызывает
только loopback Agent Herder `new_or_resume`. Notify подписанными GET опрашивает
тот же durable Hub job до terminal state, отмечает delivery `sent` и сохраняет
bounded receipt с прежним Agent Herder session ID. Повтор того же producer key,
параллельный duplicate и рестарт Notify/Hub не создают второй Hub job или native
session delivery. Dangerous payload fields дают 400/403 и не достигают очереди.

Mac canary выполняется только после server-100 acceptance и доказывает тот же
контракт на `shell:MacBook-Pro-User.local`, отдельном profile/route secret и
локальном Agent Herder. Финальный parity canary: create/list/update/delete и
job status доступны одинаково в web UI, Hub MCP/plugin и CustomGPTAction.
Retry не является мутацией terminal job: producer повторяет исходное событие с
тем же `Idempotency-Key`, получает те же IDs и не создаёт side effect.

## Confirmed scope

- Test-first реализовать Notify token-scoped `agent_job` delivery, HMAC ingress,
  signed terminal polling и bounded receipt без schema migration.
- Установить host-local helper/profile и route для server-100, затем Mac; каждый
  host получает отдельно названные credentials и rollback copy.
- Доказать crash/retry/idempotency state machine на реальных Notify, Hub,
  ShellMCP и Agent Herder runtime surfaces.
- Добавить одинаковые bounded webhook route CRUD и job-status операции в admin
  web UI, Hub MCP/plugin и CustomGPTAction; event payload никогда не определяет
  authority, а retry остаётся producer-owned idempotent replay.

## Explicit exclusions

- Не включать произвольное удалённое выполнение команд из внешнего webhook.
- Не менять текущий OAuth, HAOS или release runtime.
- Не принимать из event поля `target`, `command`, `url`, `harness`, `cwd`,
  `prompt`, MCP/tool identity или credentials; разрешён только job ID из scope.
- Не открывать новый webhook ingress и не использовать payload-controlled
  callback URL. Terminal result возвращается через HMAC-authenticated polling
  существующего route-owned `/webhook-jobs/{id}`.
- Не синхронизировать посторонние P0.2 task records или чужие staged изменения:
  пользователь явно выбрал эту active goal, а shared-worktree state сохраняется.

## Классификация и оценка

- Classification: Full.
- Initial optimistic active-minute estimate: 90 minutes.
- Initial likely active-minute estimate: 180 minutes.
- Initial pessimistic active-minute estimate: 360 minutes.

## План (RU)

1. Исследовать существующие notify/Hermes, Hub и agent-herder границы.
2. Сформировать три варианта архитектуры с canary и правами.
3. Получить явный выбор пользователя до реализации.

## Выбранный этап реализации (RU)

1. Test-first добавить в Agent Herder типизированное создание OpenCode-сессии
   и атомарный `new_or_resume` по стабильному имени и фиксированному CWD.
2. Исключить дубли при параллельных событиях и возвращать bounded receipt:
   `created|resumed`, session ID, delivery status и error без credentials.
3. Поднять Agent Herder web UI на ожидаемом server-100 loopback-порту и
   проверить публичный HTTPS после GPTAdmin cookie auth.
4. Проверить реальный OpenCode canary `repair_100` на server-100; webhook/Notify
   wiring остаётся следующим подэтапом этого же Normal-плана.

## Выбранный этап Notify E2E (RU)

1. Добавить в Notify durable delivery-channel для одного конфигурационно
   allowlisted agent-job: событие выбирает только известный job ID, но не
   target, command, URL, harness, CWD или свободный prompt.
2. Notify подписывает JSON HMAC-заголовками GPTAdmin webhook и передаёт
   стабильный delivery key как `Idempotency-Key`; после accepted receipt Notify
   подписанно опрашивает route-owned job до terminal state. Crash/retry повторяет
   POST с тем же ключом и получает исходный job, а не новый side effect.
3. GPTAdmin route фиксирует `shell:roomhacker-server-100` и запускает один
   установленный helper. Helper читает локальный allowlist profile и вызывает
   loopback Agent Herder `new_or_resume`; event используется только как данные
   в фиксированном сообщении.
4. После server-100 create/retry/poll/restart canary повторить тот же
   контракт на зарегистрированном `shell:MacBook-Pro-User.local`, не расширяя
   payload authority.
5. После двух live host canaries добавить web/MCP/CustomGPTAction parity для
   bounded route CRUD и job status; explicit retry того же idempotency key
   проверяется через producer и не становится terminal-job mutation.

## Parity matrix and retry boundary (RU)

| Операция | Web UI / REST | MCP / plugin | CustomGPTAction |
|---|---|---|---|
| List routes | `GET /webhook-routes` | `webhook_routes_list` | `listWebhookRoutes` |
| Create route | `POST /webhook-routes` | `webhook_route_create` | `createWebhookRoute` |
| Replace route | `PUT /webhook-routes/{id}` | `webhook_route_replace` | `replaceWebhookRoute` |
| Delete route | UI alertdialog + `DELETE` | `webhook_route_delete` + `confirm=true` | `deleteWebhookRoute` |
| Read one job | `GET /admin/api/webhook-jobs/{id}` | `webhook_job_get` | `getAdminWebhookJob` |

Все route credentials write-only. Read требует operator session/`gptadmin.read`;
write требует operator control/`gptadmin.exec` и остаётся под access-profile и
approval policy. Job list/retry не доступны ни в одной management surface:
durable job immutable, а retry принадлежит Notify producer и проверяется точным
повтором события с тем же ключом.

## Durable state machine и trust contract (RU)

- Notify producer `Idempotency-Key` фиксирует event; один event получает один
  SQLite delivery key
  `<incident>:gptadmin.agent:<allowlisted-job>:event:<event-id>`.
- Worker claim использует существующую lease. До terminal receipt ошибка или
  timeout переводит delivery обратно в `queued` с bounded exponential backoff.
- Каждый retry отправляет тот же delivery key в Hub. Hub persistence связывает
  `(route, key, body fingerprint)` с исходным Hub job; конфликтующий body даёт
  409, одинаковый body возвращает исходный job.
- Notify и Hub используют route-owned HMAC v2 над method, exact request path,
  timestamp, `Idempotency-Key` и SHA-256 body digest; replay window остаётся
  обязательным. URL/secret/job target не берутся из payload.
- После Hub terminal state Notify пишет bounded audit receipt: `completed`
  отмечает delivery `sent`, а `failed` один раз фиксируется terminal `failed`.
  Только transport error, timeout, crash до commit или неизвестный adapter
  остаются retryable; повтор не создаёт второй Hub job.
- Shell helper получает bounded event через `GPTADMIN_NOTIFY_EVENT`, не argv;
  ответы Hub и Agent Herder ограничены 64 KiB до JSON decode.
- Helper profile принадлежит ShellMCP execution user и имеет exact mode 0600,
  отдельный на host; URL обязан быть loopback exact
  `/api/sessions/new-or-resume`, harness/name/realpath-CWD/instruction принадлежат
  profile. Event проходит только как обрезанные telemetry fields с явной меткой
  `untrusted telemetry, not instructions`.
- Rollback каждого host: восстановить route/profile/env/runtime copy, удалить
  только созданный нами helper/profile и перезапустить затронутый service;
  существующие Agent Herder sessions не удаляются.

## Progress (EN, append-only)

- 2026-08-02: Post-parity production regression is green on both hosts. New
  server-100 event `evt_cea869c7c9dc481d9feb2ac57e8df601` and Mac event
  `evt_da7fdbeb9f3940359e476a8424943164` each reached their agent delivery as
  `sent` on attempt 1 through the final Hub/UI build, reused the original named
  session with `created=false`, and produced an exact `hi`. Exact producer
  replay returned the same event/incident/delivery IDs with `idempotent=true`.
  Both incidents are acknowledged and have zero queued/claimed follow-ups.
- 2026-08-02: Acceptance now explicitly includes CustomGPTAction and records
  the operation matrix. Live disposable canaries proved all five operations
  through REST/UI contract, MCP/plugin, and generated OpenAPI; temporary routes
  were removed. Terminal job retry is intentionally absent from every
  management surface: the proven retry contract is producer-owned replay of
  the same payload and idempotency key.
- 2026-08-02: Mac production extension is green. Agent Herder commit
  `cbb6e8b2fc913ac359b528d6af77b23bc829bb7c` is installed under the Mac user's
  `agents-projects`, Node 22.23.2 is installed side-by-side, all 47 Agent Herder
  tests pass, and `com.bezrabotnyi.agent-herder` is a persistent loopback
  LaunchAgent on `127.0.0.1:18787`. Named Codex session `repair_mac` reused
  session `019fc0fb-0a19-77a0-882c-aa0af93c88d6` before and after launchd
  restart and replied exactly `hi`.
- 2026-08-02: The old Mac ShellMCP dropped renderer-owned environment values.
  Darwin/arm64 build `140-notify-webhook-env.2` was deployed atomically with
  SHA-256 `bb438743f66b40e0791c73e5a60e87e91a4b1a7494cd6531f590c3b79b00232e`;
  the prior binary is retained at
  `/opt/gptadmin/rollback/notify-agent-20260802/shellmcp-before-env2`. The
  dedicated `infra.mac` producer and HMAC-v2 route then completed public Notify
  event `evt_96df7768dbee43c3a97c08b3d1ea9e91`, delivery
  `dlv_14a8e235d9c347b18f5f56066fcee5dd`, and Hub job
  `c8b5ddd7ec2cc37fc5a9f866ae144b1a` on attempt 1 with the existing session ID.
  Exact replay returned the same IDs, the assistant replied exactly `hi`, and
  both the expected failed pre-upgrade canary and the final canary were ACKed
  with no queued/claimed follow-ups.
- 2026-08-02: UI/API parity backend is test-first in progress. Shared route
  CRUD and operator job-read primitives now back REST and five MCP tools;
  read/write access policy, write-only secret schemas, admin job authentication,
  and both generated/static OpenAPI operations have focused green tests. The
  React page is independently owned by a Worker and remains under integration.
- 2026-08-02: UI/API parity is integrated and live. The Russian
  `Вебхуки и агенты` page supports list/create/replace/delete, write-only route
  and callback credentials, explicit delete confirmation, and operator job
  inspection. MCP advertises the same five operations and OpenAPI exposes the
  matching Custom GPT Action operations. Full local gates pass: Go Hub suite,
  ShellMCP suite, 22/22 React tests, production build, lint, YAML parse, and
  diff-check. A final live-schema probe caught malformed indentation in the
  generated Action OpenAPI; a full-document YAML regression was added, the
  indentation was fixed, and the Hub suite was rerun. Reviewer then caught two
  release-blocking parity defects: OAuth Action paths
  were advertised but forbidden, and root `/mcp` writes skipped approval and
  bounded-autonomous gates. Red/green regressions now cover OAuth scope/profile
  enforcement, ask-before-write approval consumption, and bounded budget
  exhaustion. Both REST Actions and MCP use the policy gates; all three REST
  writes expose the one-shot `X-GPTAdmin-Approval-ID` retry header. Production
  Hub `140-notify-webhook-ui.4` has SHA-256
  `f25ed00bc4447183f1be7c28a9fbca4107303a4160a0c4d494b2a3ec5f64f635`;
  rollback is
  `/opt/notify/.rollback/agent-job-20260802T043821Z/ui-mcp-20260802T061726Z`.
  Live temporary-route canaries completed full REST CRUD and MCP
  create/list/replace/delete/job-read, returned no route secrets, advertised all
  five tools/operations, and cleaned up to exactly the two production routes.
  A disposable live OAuth credential then proved route list `200`, create
  `201`, delete `204`, and MCP list `200` without RPC error; the create response
  was secret-free, the route was removed, the credential was revoked, and its
  next request returned `401`. Both deployed OpenAPI documents parse and expose
  the approval header on all three writes.
  Deployed UI asset hashes match the tested `admin-ui/dist` artifacts.
- 2026-08-02: Estimate revision: release review added 45-90 active minutes after
  live proof exposed malformed generated YAML and Reviewer found two P1 policy
  gaps. Evidence: new full-document YAML parse, OAuth scope/profile tests,
  approval/budget gate tests, production `.4`, and disposable OAuth cleanup.
  The narrow OAuth path authorization is a prerequisite of the already
  confirmed CustomGPTAction parity; no broader OAuth or admin API access was
  opened.
- 2026-08-02: Final server-100 production canary is green after the fail-closed
  Hub and ShellMCP environment fixes. Public Notify event
  `evt_e1fbb1bb29f1445799888268bb3187ec` created agent delivery
  `dlv_0960c0dc7a4441658354cdf2e0ac65ed`; it reached `sent` on attempt 1.
  Durable receipt records Hub job `b56aac694edbca6e0b22e713c3734fa9`,
  ShellMCP return code 0, existing Agent Herder session
  `019fc06d-10da-7cd3-a986-ee2d5e884edc`, `created=false`, and the assistant
  reply was exactly `hi`. Exact producer replay returned the same event,
  incident, and delivery IDs. A payload-owned `target` was rejected with HTTP
  400 and created no delivery. After restarting both Notify and Hub, the exact
  replay remained idempotent, Hub state remained completed, the agent delivery
  count did not change, and no new Agent Herder message appeared. Both canary
  incidents were acknowledged and have no queued/claimed follow-ups.

- 2026-08-02: The operator explicitly granted context-mode global read access
  to the global Codex instruction file outside project workspaces. The global
  host permission scope is deliberately limited to read-only access for
  `/home/roomhacker/.codex/AGENTS.md`; LastHumanCommit role instructions were
  already covered by the existing read-only global allow rule.

- 2026-08-02: Task opened as Full because it crosses webhook ingress, remote
  execution, UI and AI control planes. No implementation or deployment has
  started.
- 2026-08-02: Read-only research confirmed an existing signed, idempotent Hub
  webhook gateway and operator HTTP CRUD, but no Notify-to-Hub result bridge,
  no MCP CRUD parity, and no proven server-100/Mac agent target contract.
- 2026-08-02: Independent Overseer verdict RETHINK: implementation is blocked
  pending a user-selected architecture and answers on trusted ingress, the
  fixed OpenCode job contract, AI-surface scope, and target readiness. The
  immutable safety boundary is fixed rule ID, allowlisted job ID and registered
  target; payloads may not carry a command, target address, MCP name/tool,
  credential or free-form prompt.
- 2026-08-02: Estimate revision (evidence: cross-host agent-runner and Notify
  callback contracts are absent from verified sources): optimistic 120,
  likely 240, pessimistic 480 active minutes. No runtime changes made.
- 2026-08-02: Read-only live diagnosis: `agent.bezrabotnyi.com` reaches the
  cookie-login redirect, but its authenticated Nginx upstream is
  `127.0.0.1:18787`; server-100 has no listener or Agent Herder unit there.
  OpenCode is live at `127.0.0.1:4095`. Agent Herder's optional web UI starts
  only when `AGENT_HERDER_WEB_PORT` is set, so the public vhost is a stale
  deployment contract, not an active service. No repair was performed.
- 2026-08-02: User selected the concrete Normal-stage contract for Agent Herder:
  create a named OpenCode session or resume the existing one, then deliver the
  event message. Implementation authorization includes restoring its web UI and
  a server-100 canary; full Notify/GPTAdmin route wiring remains a later slice.
- 2026-08-02: Estimate revision for this selected slice: optimistic 60, likely
  120, pessimistic 240 active minutes; trigger: implementation and live deploy
  were explicitly requested after the planning gate.
- 2026-08-02: Overseer returned RETHINK; Lead resolved the contract before RED
  tests: both OpenCode and Codex are supported; identity is the exact tuple
  `(harness, canonical_cwd, name)`; canary CWD is the verified server-100 Git
  worktree `/home/roomhacker/ServersAdministartion`; duplicate matching legacy
  sessions fail closed; atomicity is guaranteed within the single managed
  Agent Herder process and survives restart by rescanning native names.
- 2026-08-02: Message semantics are one delivery attempt per Agent Herder call;
  GPTAdmin owns event idempotency. Queue mode reports native acceptance, sync
  mode reports completed adapter response. Trusted MCP/UI callers may supply a
  message; future disk-full webhook policy must render a fixed template from
  allowlisted fields. OpenCode SDK and generated Codex app-server schemas
  independently prove the native create/name protocol shapes.
- 2026-08-02: Second Overseer RETHINK resolved before implementation: `harness`
  is an explicit required request field; `cwd` is required, absolute and
  realpath-canonicalized; duplicates are only multiple exact
  `(harness, canonical_cwd, name)` matches, while other CWDs/harnesses are
  independent. The managed server-100 production route uses one Agent Herder
  HTTP backend process; native session-name rescan preserves reuse after its
  restart. Creation followed by delivery failure returns
  `{ok:false, created:true, sessionId, delivery:"failed"}` and never hides the
  surviving session.
- 2026-08-02: Slice canary/rollback: prove create and second-message reuse for
  OpenCode and Codex, duplicate fail-closed, restart reuse, listener
  `127.0.0.1:18787`, public authenticated 200, and direct MCP startup. Before
  deploy preserve the prior package/unit state; rollback stops the new unit and
  restores that exact state. No canary claim is a Notify/GPTAdmin E2E claim.
- 2026-08-02: Implemented and pushed Agent Herder MCP/HTTP/UI parity for
  `create_session` and `new_or_resume`, native OpenCode and Codex session
  creation, canonical `(harness, cwd, name)` matching, duplicate fail-closed,
  restart-safe native rescan, cross-process locking, and bounded delivery
  receipts. Commits: `964c274`, `8db57d7`, `cbb6e8b` on
  `agent/session-lineage-tools`.
- 2026-08-02: Live server-100 deployment restored the previously absent
  `agent-herder.service` and listener `127.0.0.1:18787`; local web root is 200
  and public unauthenticated ingress now redirects to the expected cookie login
  instead of returning 502. Direct MCP probe advertised both new tools (15
  tools total).
- 2026-08-02: Initial live OpenCode canary exposed a project-scope defect:
  `/session` without `directory` could not see a named session outside the
  OpenCode server CWD and created a duplicate. A failing regression reproduced
  the missing CWD request; the adapter contract now passes canonical CWD to
  native discovery. The accidental second canary-only OpenCode session was
  deleted (DELETE 200, subsequent GET 404), leaving the original session.
- 2026-08-02: Final build/test evidence is 12/12 files and 47/47 tests; focused
  CWD discovery tests are 15/15; `git diff --check` passed; Graphify updated to
  577 nodes, 1232 edges, and 30 communities. Independent Reviewer verdict:
  APPROVE with no P1/P2 findings.
- 2026-08-02: Final live canary after service restart reused the original IDs
  with `created:false` and `delivery:"accepted"`: OpenCode
  `ses_03f93051fffeC9FCtPqwA0EyrN`, Codex
  `019fc06d-10da-7cd3-a986-ee2d5e884edc`. Native exact-match count is one per
  harness and both transcripts contain the post-restart assistant reply `hi`.
  Browser-authenticated public 200 was not independently replayed because the
  automation browser had no auth cookie and desktop Touchpoint transport was
  unavailable; upstream local 200 and public login redirect are proven.
- 2026-08-02: Estimate revision: the selected Agent Herder slice consumed the
  pessimistic range because live deployment uncovered OpenCode's directory-
  scoped listing contract and required a test-first follow-up. Product task
  remains active for the explicitly deferred Notify/GPTAdmin routing and Mac
  slices; no full webhook E2E is claimed.
- 2026-08-02: Continuation audit confirmed the reusable Hub primitives already
  exist: HMAC ingress with a five-minute replay window, durable route/job state,
  event idempotency, bounded callback retry, and policy-gated fixed MCP/Shell
  actions. No second public ingress will be added.
- 2026-08-02: Live topology fixed the targets without credentials:
  `shell:roomhacker-server-100` and `shell:MacBook-Pro-User.local` are both
  registered online at the Hub. Agent Herder is active on server-100 loopback;
  Notify production is active from `/opt/notify` and public root is 200 with
  authenticated health returning the expected unauthenticated 401 boundary.
- 2026-08-02: Estimate revision for the full Notify callback plus two-host
  rollout: optimistic 120, likely 240, pessimistic 420 active minutes. Trigger:
  current evidence shows missing Notify outbound/callback adapter, missing
  server-100 helper/profile, and an unreachable Mac SSH path even though its
  ShellMCP target is online.
- 2026-08-02: First public server-100 canary exposed a fail-open runtime gap:
  ShellMCP returned `returncode=1` because the renderer-owned event environment
  name differed, while Hub classified transport completion as action success.
  No Agent Herder message was delivered. RED regressions now require a verified
  zero exit and support the renderer-owned bounded environment value directly.
- 2026-08-02: Server-100 sandbox proof retained `ProtectHome=true`. The helper
  now validates a profile-owned absolute CWD without reading the protected home;
  Agent Herder remains the canonical existence/realpath authority outside the
  ShellMCP sandbox. The temporary exact bind drop-in is removed.
- 2026-08-02: Completion receipt. Notify `main` contains `7fb5605`; GPTAdmin
  `main` contains `04980ac` and annotated tag `v141`. Build, Sync, Release run
  `30737173119` completed successfully at exact source commit `04980acfd3d6f9924177683dac86dee5727eb959`;
  failover, Windows, macOS, admin UI, and build/release jobs are all green.
  Public release `v141` is neither draft nor prerelease and exposes 12 assets
  with GitHub SHA-256 digests. Its manifest is schema
  `gptadmin.release-manifest/v1`, `build_version=141`, the same source commit,
  and 12 artifacts. Overseer PASS, Reviewer APPROVE after both P1 fixes, and
  final Critic PASS are recorded in the task evidence above. P0 CONFIRMED.
- 2026-08-02: Completion audit reopened the task. Public `v141` had 12 release
  assets but omitted `gptadmin-win.zip`, while its provenance manifest listed
  that ZIP twice (the identical `build/` and `public/` copies). Root cause is
  the release upload allowlist matching only `*.tar.gz`, manifest, and SBOM.
  The green Windows job did not upload an Actions artifact, so recovery must
  rebuild the exact tagged ZIP and prove its SHA-256 before attaching it. P0 is
  not confirmed until manifest/release names and digests agree.
- 2026-08-02: Release recovery completed without rebuilding or moving the tag.
  The immutable public `v141` source tag already contained the exact generated
  `public/gptadmin-win.zip`; its 3,637,301-byte payload matched provenance
  SHA-256 `0f6428a10e3ceccd165d36528e8f050c7e803eb2a80548bb8a6496616b8358fb`
  before upload. Public Release `v141` now has 13 assets: the 11 unique manifest
  archive basenames plus `manifest.json` and SBOM, with no missing, unexpected,
  or digest-mismatched assets. A RED/green workflow contract now requires
  `build/*.zip` in every future immutable release asset set; focused release
  tests pass 12/12.
- 2026-08-02: Final current-state runtime audit reconfirmed both HMAC-v2 routes
  at 300-second skew, bounded-autonomous policy, fixed ShellMCP targets, and no
  route bearer tokens. Both ShellMCP targets are online. Server-100 and Mac
  durable agent deliveries remain `sent` on attempt 1 with no error, their Hub
  jobs are `completed`, and the receipts record `created=false` with the same
  named Codex session IDs. Both sessions are currently readable from Agent
  Herder and idle; server-100 user service/listener and Mac launchd service are
  running. The server-100 completion timestamp precedes Mac as required. P0
  CONFIRMED after release recovery.
