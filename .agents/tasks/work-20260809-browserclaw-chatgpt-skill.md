# Задача: skill для BrowserClaw и GPTADMIN в настоящем ChatGPT

## Исходный запрос

Записать проверенный сценарий BrowserClaw на Mac mini как skill; автоматизацию
обсудить позже.

## Цель

Создать автообнаруживаемый Codex skill для повторения read-only/browser acceptance
проверки GPTADMIN plugin в настоящем залогиненном ChatGPT через BrowserClaw MCP.

## Бизнес-canary

Skill должен позволять безопасно повторить: BrowserClaw handshake на Mac mini,
открытие GPTADMIN, выбор плагина в новом чате и отправку тестового запроса; при
зависшем ответе обязан явно зафиксировать failure, а не объявлять E2E успешным.

## Подтверждённый scope

- skill размещается в `/home/admin/.codex/skills/browserclaw-chatgpt-acceptance`;
- Mac mini: `203.0.113.10`, SSH alias `whitetransport-mac-mini-2012`;
- BrowserClaw MCP: `http://127.0.0.1:9010/mcp` на Mac mini;
- skill описывает сохранение одной MCP-сессии и ownership вкладок;
- skill остаётся инструкцией, без production rollout и без автоматизации CI.

## Явные исключения

- не сохранять Bearer, cookies, ChatGPT transcript или MFA;
- не создавать и не перезаписывать реальный Custom GPT;
- не менять GPTAdmin, BrowserClaw, Mac mini или production-конфигурацию;
- не утверждать успешный browser E2E по одному handshake или прямому connector-вызову.

## Оценка

Первоначальная оценка: 20 активных минут. Пересмотры добавляются append-only с
причиной и evidence.

## План

1. Инициализировать skill стандартным генератором.
2. Записать компактный SKILL.md с подтверждёнными topology и failure guards.
3. Сгенерировать UI metadata и прогнать quick validation.
4. Зафиксировать результат и оставить автоматизацию отдельной следующей задачей.

## Evidence (2026-08-09)

- Skill created at `/home/admin/.codex/skills/browserclaw-chatgpt-acceptance`.
- Added `SKILL.md` with Mac mini/BrowserClaw topology, single-session ownership,
  fresh-ref rules, plugin selection, approval boundary, failure classification
  and secret-safe evidence requirements.
- Added `agents/openai.yaml` metadata.
- `quick_validate.py` passed: `Skill is valid!`.
- Automation/CI intentionally not implemented in this task.
