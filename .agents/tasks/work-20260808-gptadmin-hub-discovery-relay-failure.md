# GPTAdmin hub discovery/relay failure

Status: in_progress

## Исходный запрос

Пользователь сообщает, что Custom GPT не смог достоверно собрать список серверов: `listMcpServers` / `hub.discover` падают с `aiohttp.client_exceptions.ClientResponseError`, `shell:server01` запускает job, но результат фоновой job падает через hub, `shell:admin-server-100` отвечает `unknown shell server`, а до server-88 через relay не достучаться. Нужно понять, какой баг у GPTAdmin.

## Objective

Определить подтверждённый участок отказа в GPTAdmin hub/relay и, если причина локальна и безопасно исправима без consequential action, подготовить узкий фикс с regression evidence.

## Business canary

Через реальный Custom GPT/MCP путь: discovery возвращает инвентарь без 5xx; `shell:server01` возвращает stdout фоновой job; зарегистрированные server-100 и server-88 корректно разрешаются по каноническим именам.

## Confirmed scope

- `/home/admin/gptadmin` hub, discovery, shell registry and relay error path.
- Read-only локальная диагностика и focused tests.

## Explicit exclusions

- Не утверждать состояние systemd на удалённых серверах без stdout.
- Не перезапускать/деплоить hub или relay без отдельного разрешения.
- Не менять секреты, ACL, маршрутизацию или unrelated dirty work.

## Initial active-minute estimate

- Optimistic: 15 min
- Likely: 30 min
- Pessimistic: 60 min

## План (первичный)

1. Зафиксировать локальный статус и найти реализацию discovery/job/registry/relay.
2. Воспроизвести ошибку локальными тестами или безопасным read-only вызовом.
3. Сопоставить фактический traceback с контрактом и выдать подтверждённую причину или точный внешний блокер.

## Evidence

- Local focused Go tests for discovery and shell relay passed: `go test ./internal/hub -run 'TestDiscoveryPublishesShellMCPChildAsDirectServer|Test.*Relay.*Job|Test.*Shell.*Server' -count=1` -> `ok`.
- Public live `https://became.bezrabotnyi.com/version` -> HTTP 200, build 145, commit `c970f73`; `/healthz` -> HTTP 200. This proves the public process is reachable, not that the authenticated business canary works.
- Public unauthenticated `/mcp-relay/servers` -> HTTP 401 as expected, with OAuth resource metadata pointing to `https://your-subdomain.t.became.bezrabotnyi.com`.
- Local `gptadmin auth-diagnose` -> `Public origin: http://127.0.0.1:9001`, `MCP resource: http://127.0.0.1:9001`, verdict `configured origin/resource mismatch`; the live Hub advertises the public HTTPS origin/resource.
- The local configured custom bearer was not accepted by the public Hub: authenticated read-only probes to `/mcp-relay/servers` and `/admin/api/overview` both returned HTTP 401. No token value was printed.
- Source contract publishes `operationId: discover`; `listMcpServers` remains an internal compatibility alias. Therefore a Custom GPT schema/client using the live OpenAPI should call `discover`, while stale cached schema or stale OAuth credentials can produce the observed HTTP-client exception.

## Current diagnosis

Confirmed GPTAdmin-side auth/configuration mismatch in the local client environment; public Hub process and local discovery implementation are live/green in isolation. The exact Custom GPT OAuth token/session and its request trace were not available, so the final claim that its specific `ClientResponseError` is caused by this token cannot be proven from this turn alone.

## Recovery verification: 2026-08-08

- Public `your-subdomain.t.became.bezrabotnyi.com/version` is HTTP 200 with build 145 / commit `c970f73`.
- Normal TLS verification passes on the active DNS edge `185.240.120.152`; the alternate VPN2 edge `212.192.31.128` also passes when selected explicitly.
- Public OAuth protected-resource and authorization-server metadata are reachable and advertise the same HTTPS resource/issuer with PKCE S256.
- Unauthenticated relay and child-MCP probes correctly return HTTP 401, so the protected routes are present rather than dead.
- The actual GPTADMIN connector still fails before `gptadmin_discover` with `oauth_token_invalid_grant` / `reauthentication_required` (`TRIGGER_REAUTHENTICATION`). This is the immediate reason the plugin and Custom GPT cannot make authenticated calls; a server restart cannot repair the revoked client refresh session.
- The old `u-58e0f4e4...` hostname still resolves to legacy/fallback edges including a self-signed `incident-fallback` path, but it is not the active DNS target of `your-subdomain...`; do not use it for the connector configuration.
- Repository verification: local branch is `main`, `HEAD` and `origin/main` are both `26dcdec`; the difference from the installed functional build `c970f73` is the later documentation-only TLS evidence commit, not an unbuilt code branch.
- Live admin verification: `/admin/login` and `/admin/` return HTTP 200 with the GPTAdmin Login HTML; `/admin/api/overview` returns the expected HTTP 401 without credentials; live service is active and local `/version` is build 145 / `c970f73`.
- Admin password verification: the supplied password matches the configured `ADMIN_PASSWORD`; direct local Hub login returns `302` and a session cookie. The canonical public `your-subdomain.../admin/login` POST returns `200` with no cookie on both tested ingress IPs, indicating the external ingress drops or rewrites the form POST. The legacy `became.bezrabotnyi.com/admin/login` returns `302` plus cookie with normal TLS, so the password and Hub auth implementation are valid.
- Do not switch `PUBLIC_ORIGIN`/`MCP_RESOURCE` to the legacy hostname as an unreviewed workaround; OAuth metadata and Custom GPT configuration are intentionally bound to the canonical `your-subdomain...` resource. The remaining repair belongs at the canonical ingress.
- Bearer/OAuth regression proof: the user-supplied GPTK Bearer is accepted by `became.bezrabotnyi.com` (`/mcp-relay/servers` HTTP 200 and `/mcp` HTTP 200) but rejected by canonical `your-subdomain...` (`/mcp-relay/servers`, `/admin/api/overview`, and `/mcp` HTTP 401). The full PKCE authorize GET renders on both origins; posting the same valid admin password and hidden PKCE fields yields `302` with authorization code on legacy, but canonical returns HTTP 403 with an opaque binary response. This proves canonical ingress/backend auth state and OAuth POST path are broken independently of the password or Bearer header format.
- Historical project-chat/task evidence confirms this was previously fixed: commit `9c5848e` corrected the old installed CLI JWT (`resource` and `kid` claims), and the recorded release-145 canary passed Custom GPT `health → version → connection → oauth → openapi → mcp` plus per-edge BrowserClaw/MCP checks on all three prior A-records. That prior proof used the then-converged 9c5848e topology. Current `u-f...` is now on build `c970f73` at `185.240.120.152`, while `gptadminmcp...` resolves separately to `203.0.113.10`; the current cross-origin Bearer/OAuth mismatch is therefore a topology/state regression after the previously green fix, not a rediscovery of the old JWT bug.

## Follow-up: applying settings through ChatGPT UI

- User authorized applying the new settings in ChatGPT without manual repetition.
- Browser-control attempt via the available Touchpoint connector failed immediately with `Transport closed` for windows/apps/diagnostics.
- Only isolated headless Playwright Chrome processes were visible; no controllable interactive ChatGPT window or alternate browser connector was available.
- No ChatGPT setting, schema, OAuth session, or token was changed.

## Follow-up: GPTADMIN MCP connector result

- The actual GPTADMIN MCP connector was invoked for `gptadmin_discover`.
- It failed before discovery with `UNAUTHORIZED`, `oauth_token_invalid_grant`, `reauthentication_required`, and `TRIGGER_REAUTHENTICATION`.
- Therefore BrowserOS cannot be selected or used from this session until the GPTADMIN app connection is reauthenticated. No BrowserOS or ChatGPT setting was changed.
