# TODO: notify Telegram route test regression

- Symptom: full pytest run for the NoticePlace workspace reported 146 passed and 1 failed in `/home/admin/agents-projects/notify/tests/test_telegram_controls.py::TelegramControlPolicyTests::test_severity_route_overrides_default_chat_and_optionally_sets_topic`.
- Smallest evidence: expected `{'chat_id': 'default-chat'}` but received `{'chat_id': 'notice-chat', 'message_thread_id': '17'}`.
- Scope/blocker: unrelated to the health orchestration change; do not investigate or alter it during this task unless explicitly selected.
