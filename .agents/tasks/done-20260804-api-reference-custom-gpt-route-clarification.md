# Задача: поправить `docs/API_REFERENCE.md` по Custom GPT / CTL

- Оригинальный запрос: исправить оставшиеся противоречия в `docs/API_REFERENCE.md` без трогания других файлов.
- Цель: явно закрепить, что канонический Custom GPT schema путь — `/actions/openapi.yaml` (или per-server), relay calls идут через `/mcp-relay/*`, а `CTL_TOKEN` остаётся только legacy/admin migration detail.
- Бизнес-канарейка: docs/product auth тесты должны пройти без регрессии, а текст API reference должен больше не приписывать Custom GPT пути к `CTL_TOKEN` или `/api.json`/`/openapi.yaml`.
- Подтверждённый объём: только `docs/API_REFERENCE.md`.
- Явные исключения: не трогать другие файлы, не менять runtime/код, не переписывать соседние docs.
- Initial estimate: 15 min.
- Revisions: none.
