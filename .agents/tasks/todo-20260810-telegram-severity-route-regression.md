# Telegram severity route test regression

Status: todo
Observed: 2026-08-10

Symptom: full NoticePlace suite reports `147 passed, 1 failed` in
`/home/admin/agents-projects/notify/tests/test_telegram_controls.py::TelegramControlPolicyTests::test_severity_route_overrides_default_chat_and_optionally_sets_topic`.

Smallest evidence: actual route is `{"chat_id":"notice-chat","message_thread_id":"17"}`;
the test expected `{"chat_id":"default-chat"}`.

Blocker: this is a separate Telegram routing contract and may affect Health
topic delivery. It was not changed or investigated during the Hermes profile
fix; resolve only in a bounded Telegram/topic-routing task.

Follow-up evidence (2026-08-10): the NoticePlace test file currently exists
only at `/home/admin/agents-projects/noticeplace/tests/test_telegram_controls.py`,
where the focused three-test collection passes and the route expectation is
`notice-chat` with topic `17`. A full run still reported a traceback filename
under the sibling `notify` path, although that sibling test file was absent
when checked immediately afterward. This is currently treated as a shared
worktree/pytest collection race, not a verified Health routing defect; no
Telegram code was changed.
