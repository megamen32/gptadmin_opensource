# Monorepo: код, документация и сайт

Role: Lead
Status: implementation

## Исходный запрос

«Полноценный monorepo: импортировать сайт в `gptadmin/website/`, убрать
gitlink, перенести канонические docs в один каталог, поставить CI-проверки и
публично архивировать старый репозиторий. Только сабагентами».

## Цель

Один versioned source of truth для кода, OpenAPI, документации и сайта;
отдельный website-репозиторий публично помечен legacy после миграции.

## Бизнес-канарейка

Один commit SHA GPTAdmin определяет сайт, английскую документацию,
сгенерированные переводы и OpenAPI-контракт; CI подтверждает отсутствие
расхождения до публикации.

## Подтверждённый scope

Импорт website, удаление gitlink, один docs source, переводы RU/CN как
производные, CI-контракты, публичная legacy-декларация.

## Исключения

Не менять Hub/API-поведение, не удалять историю без явного решения о способе
сохранения, не архивировать GitHub-репозиторий до готовности replacement URL.

## Оценка

Initial estimate: 180 / 300 / 480 active minutes.

## План исследования

1. Картировать gitlink, историю и deployment website.
2. Спроектировать docs-as-code и стратегию трёх языков.
3. Спроектировать проверяемую миграцию и CI-канарейку.

## Execution graph

1. Huygens imports `website` as a history-preserving subtree (critical path).
2. Copernicus independently adds root documentation-contract CI and tests.
3. Huygens consolidates canonical docs and site consumption after the import.
4. Fresh Worker adds the public Legacy notice after the replacement site passes
   its real build canary.
5. Fresh Reviewer and Tester gate the integrated migration; external archive is
   a final explicit publication step.

