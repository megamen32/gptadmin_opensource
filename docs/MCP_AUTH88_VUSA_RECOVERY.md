# Восстановление MCP88 и старого VUSA tenant

На 88 обе старые reader-бинарные версии cb6a944 отклоняют issuer основного
Hub. Реплики уже имеют ту же generation/digest, что writer: это не отсутствие
синхронизации. Исправление b2dd161/fd91958 проверяет исходный bearer через
собственный enrolled node credential и pinned writer admission, затем сохраняет
локальные проверки profile/target/tool. Signing key не копируется на readers.

Второй runtime-дефект: PUBLIC_ORIGIN у admin-server-88 был физическим
LAN-адресом, а у server-88-unified отсутствовал. Управляемые токены из общей
реплики поэтому отвергались по issuer. Использовать существующий логический
контракт writer (PUBLIC_ORIGIN/MCP_RESOURCE/его exact legacy aliases), а
GPTADMIN_NODE_PUBLIC_URL задавать отдельно для собственного FRP endpoint узла.
Не добавлять произвольные aliases, не ослаблять issuer/audience validation.

Свежий реальный bearer-admission POST с88 через public DNS уходил в timeout5s,
а тот же запрос через203.0.113.10 с прежним HTTPS hostname/SNI/CA дал200 и
полный proof. Для обоих readers задаётся существующий supported TCP pin
GPTADMIN_NODE_WRITER_CONNECT_TO=203.0.113.10. URL/сертификат/WriterID/NodeKey
не меняются; прямой LAN не заменяет TLS или проверку криптографического proof.

Первый auth-admission rollout использовал принятый cloud Node package
1f6eea2/run38176217408. Реальный OAuth consumer после исправления authority
и LAN TLS pin выявил отдельный дефект постоянного WebSocket: MCP routing
headers теряются при frame→loopback. Исправление node_conn передаёт только
Accept/MCP-Protocol-Version/Mcp-Method/Mcp-Name; исходный bearer остаётся
отдельным полем, cookies/session IDs/arbitrary headers запрещены. JSON-RPC
body/IDs/idempotency не меняются. Нужен новый проверенный public Node artifact
с этой узкой дельтой; локальные широкие Go сборки не запускать. На100 сохранять существующий Hub budget3/10GiB, CPU2, tasks512,
swap0. Перед переключением штатно закрыть admission/drain и дождаться своих
активных заданий, сохранить binary/config/state и rollback. Не останавливать
native Codex/Herder/ShellMCP/браузеры. Оба88 reader обновлять тем же пакетом,
сохраняя каждый config, identity, очередь, membership и profiles.

Reader88 до обновления: 35–38MiB/39–40tasks на процесс. Первый конечный
бюджет каждого reader:128/256MiB, CPU1, tasks128, swap0, IOWeight10,
GOMAXPROCS1. Host reserve32GiB; startup/read/hostname измерить после обновления.
Большие child workloads этим коротким read-canary не принимаются.

Старый VUSA FRPC tenant u-1a165049 указывает на локальный Hub9001.
Сохранять именно его PUBLIC_ORIGIN, authority и прежний standalone Hub:
резервный canary19002 относится к другому tenant и не заменяет этот upstream.
Перед восстановлением подтвердить binary/source и отсутствие9001 listener.
VUSA измеренный запас≈229MiB, безswap. Existing Hub задаётся96/128MiB,
CPU1, tasks64, swap0, IOWeight10, GOMAXPROCS1; оставлять64MiB дляSSH/соседей,
disk reserve500MiB. При недостатке свежего запаса apply отклоняется.

Диагностика≤256MiB/CPU15s/wall30s, HTTP15s/response2MiB, stage≤128MiB,
apply≤120s/host; без suites/browser/параллельной сборки. Резервные копии root0700,
файлы с credentials0600. Выводить только IDs, статусы, код ошибки и hostname.

Приёмка: normal consumer на основном Hub исполняет harmless hostname на88;
полученный job читается у exact owner и повторно даёт тот же результат.
Старый VUSA tenant выполняет реальный MCP-вызов через собственный FRP vhost,
с его собственной пользовательской авторизацией, включая отрицательную проверку
анонимного доступа. Старые задачи/UNKNOWN и бизнес-действия не повторять.

После обновления writer startup reconciliation поменяла содержимое authority
без смены сохранённой generation. Реальный bearer-admission proof дал тот
же writer/generation/token_digest, но иной authority_digest. Один штатный
auth-snapshot/export опубликовал1932; profiles, clients и non-control tokens
проверены неизменными. Обе replicas получили1932 штатным membership pull,
без рестарта и ручной подмены state/digest. Проверка digest остаётся строгой.
