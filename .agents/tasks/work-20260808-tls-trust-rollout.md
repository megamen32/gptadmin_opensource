# Исправить trust chain для GPTAdmin/Mac ShellMCP и довести BrowserClaw canary

## Оригинальный запрос

Починить сломанные TLS-доверие/сертификаты после перевыпуска, раскатить новую цепочку и довести BrowserClaw/MCP live-проверку до рабочего состояния.

## Цель

Новый сертификат/CA доверен на public ingress, server-100, HAOS и Mac mini; heartbeat Mac ShellMCP проходит; direct BrowserClaw endpoint отдаёт tools и выполняет безопасный browser flow.

## Business canary

`/server/browserclaw/mcp`: initialize → tools/list → tabs list/new example.com → snapshot → close; heartbeat child health ready.

## Scope

- Диагностика реальной certificate chain и trust stores.
- Минимальная установка нового CA/цепочки на нужных хостах.
- Перезапуск только GPTAdmin Hub/ShellMCP и BrowserClaw-related LaunchAgent.
- Исправление rollout-preview redaction для `*_bearer` с regression test.

## Exclusions

- Не менять архитектуру Hub/ShellMCP.
- Не отключать TLS verification глобально.
- Не перевыпускать сертификаты заново без подтверждения фактической цепочки.

## Initial estimate

45 active minutes.

## План

1. Проверить certificate chain, issuer/SAN/expiry и локальные trust paths.
2. Найти фактический новый CA/chain на server-100/HAOS/Mac mini.
3. Установить trust narrowly, restart, verify heartbeat.
4. Исправить bearer redaction, tests, build.
5. Повторить public BrowserClaw canary и закрыть task только при полном green.

## Status

Диагностика продолжена. Новый Let's Encrypt chain на `185.240.120.152` и
`212.192.31.128` проходит обычную TLS-проверку. На Mac добавлен `ISRG Root X1`
в кастомный `roots-bundle.pem`, установлен exact Darwin ShellMCP `145/c970f73`,
heartbeat включён, auto-update отключён до исправления отдельного manifest edge,
и BrowserClaw live canary прошёл: tools/list=17, tabs new example.com,
snapshot Example Domain, tabs close.

Оставшийся внешний blocker: edge `95.165.165.65` всё ещё отдаёт
`incident-fallback` self-signed certificate для того же hostname; это не
исправляется trust-store на Mac и требует доступа к владельцу этого внешнего
edge/DNS path. Public code/Hub и два других edges уже green.
