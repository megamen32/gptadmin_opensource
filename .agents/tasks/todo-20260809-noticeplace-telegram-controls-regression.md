# Unselected defect: NoticePlace full-suite Telegram route regression

- Symptom: `python3 -m pytest -q` in `/home/admin/agents-projects/noticeplace`
  failed one existing test: `TelegramControlPolicyTests.test_severity_route_overrides_default_chat_and_optionally_sets_topic`.
- Smallest evidence: expected `{'chat_id': 'default-chat'}` but received
  `{'chat_id': 'notice-chat', 'message_thread_id': '17'}`; 132 other tests passed.
- Scope: observed while validating the health workflow; not selected for this
  task and not investigated further.
- Blocker: requires a separate owner decision because it may be an unrelated
  Telegram policy change or concurrent work.
