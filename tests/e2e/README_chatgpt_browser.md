# ChatGPT Browser Acceptance Runner

`tests/e2e/chatgpt_browser_acceptance.py` is a secret-safe runner that drives
a real ChatGPT browser session through a saved Custom GPT Action and verifies
that ChatGPT actually called the public GPTAdmin relay. It targets
`BrowserClaw` (the operator's persistent browser profile on the Mac mini);
no Playwright is needed at the operator side.

The companion regression suite is `tests/test_chatgpt_browser_acceptance.py`
(`pytest` tests for URL redaction, ingress parsing, secret-safety invariants,
and the runner CLI plumbing — does **not** touch the live browser).

---

## One-time setup: save the OAuth Action in the Admin GPT editor

```bash
GPTADMIN_BROWSERCLAW_PORT=9210 \
python3 tests/e2e/chatgpt_browser_acceptance.py --admin-save \
    --browserclaw-ssh whitetransport-mac-mini-2012 \
    --admin-editor-url  "https://chatgpt.com/gpts/editor/<GPT_ID>-admin" \
    --admin-schema-url  "https://<HUB>/actions/openapi.yaml" \
    --admin-auth-url    "https://<HUB>/oauth/authorize" \
    --admin-token-url   "https://<HUB>/oauth/token" \
    --admin-client-id   "chatgpt" \
    --admin-scope       "gptadmin.read gptadmin.exec" \
    --receipt trash/logs/admin-save.json
```

The runner:

1. Opens the editor at `--admin-editor-url`.
2. Clicks `Конфигурация` → `Создать новое действие` → `Импортировать из URL-адреса`.
3. Fills the schema URL into the import dialog and clicks the visible `Импорт`
   button (Enter is not a reliable submit action in the ChatGPT editor).
4. Clicks the `Аутентификация` chip (a `generic [cursor=pointer]` between two
   `LabelText` entries — ChatGPT hides the label from the a11y tree).
5. In the `Аутентификация` dialog, clicks `OAuth`, fills the four fields in
   document order (Authorization URL, Token URL, Client ID, Scope), then clicks
   `Сохранить`.
6. Writes `trash/logs/admin-save.json` with redacted client_id
   (`cl***nt`) and ref-only step markers. No Bearer, no Authorization header,
   no chat content is logged.

Re-run is idempotent: the next run will simply re-save the same OAuth config
on top of the existing draft.

---

## Run the T-proof: ChatGPT actually invokes `/mcp-relay/*`

```bash
GPTADMIN_BROWSERCLAW_PORT=9210 \
python3 tests/e2e/chatgpt_browser_acceptance.py --admin-chat \
    --browserclaw-ssh whitetransport-mac-mini-2012 \
    --browserclaw-custom-gpt-url "https://chatgpt.com/g/<GPT_ID>-admin" \
    --action-origin "https://<HUB>" \
    --admin-chat-prompt "Вызови Discover и uptime на всех серверах через GPTAdmin." \
    --consent-timeout 300 \
    --post-consent-wait 180 \
    --require-call-200 \
    --receipt trash/logs/admin-chat.json
```

What happens:

1. The runner opens a fresh tab at `--browserclaw-custom-gpt-url`. Because the
   BrowserClaw persistent profile is already logged in, the new tab inherits
   the operator's ChatGPT session.
2. Sends the prompt through the chat composer.
3. Watches for **every** auth gate that ChatGPT shows and clicks it:
   - "Войти в систему с `<domain>`" (OAuth provider sign-in)
   - "Разрешить" / "Allow" — ChatGPT shows one of these per tool call,
     so the runner loops for the whole `--post-consent-wait` window.
4. After the budget is exhausted it reads the nginx access log filtered to
   `ChatGPT-User`, compares to the baseline captured before step 1, and writes
   the receipt.

If `--require-call-200` is set the runner exits non-zero when no 200 status
was observed on `/mcp-relay/call` (during the run or in the historical
baseline).

### `--auth-mode bearer`

Swap `--auth-mode bearer` for ChatGPT Actions whose OpenAPI uses an HTTP
`bearer` security scheme. The runner waits for the "Enter API key / Bearer
token" paste dialog and reports when the operator has pasted the token (it
does NOT auto-paste — that would leak the secret into the runner process).

---

## What the runner measures

The receipt JSON contains:

```json
{
  "baseline_ingress":  {"by_path_status": {...}, "by_status": {...}, "total": N},
  "after_ingress":     {"by_path_status": {...}, "by_status": {...}, "total": M},
  "delta_by_path":      {"/mcp-relay/servers": X, "/mcp-relay/tools": Y,
                          "/mcp-relay/call": Z},
  "discover_delta":     X,
  "tools_delta":        Y,
  "call_delta":         Z,
  "call_path_observed": true|false,
  "call_status_codes":  [200, 403],
  "call_200_observed":  true|false,
  "auth_gate_kind":     "oauth_consent" | "sign_in_with_provider" | "bearer_paste" | "",
  "steps":              [...],
  "status":             "observed",
  "started_at":         "...Z",
  "finished_at":        "...Z"
}
```

* `delta_by_path["/mcp-relay/servers"]` ≥ 1 means ChatGPT hit `discover`.
* `delta_by_path["/mcp-relay/tools"]` ≥ 1 means ChatGPT hit `schema`.
* `delta_by_path["/mcp-relay/call"]` ≥ 1 means ChatGPT actually executed a tool.
* `call_200_observed` is True only when a new `POST /mcp-relay/call` from this
  run returned 200. Historical 200s are reported separately and cannot satisfy
  `--require-call-200`.

---

## Operator pre-requisites

1. **BrowserClaw must be running** on the Mac mini with the operator logged in
   to `https://chatgpt.com/` via the persistent profile.
2. **sudo -n** must work for `grep`/`cat` on `/var/log/nginx/access.log`
   (the runner reads it to count `ChatGPT-User` calls). Without sudo the
   runner still completes but ingress verification reports "skipped".
3. **BrowserClaw MCP port** changes after every app restart. Probe it with:
   ```bash
   for p in 9010 9210 9211; do
     curl -s -o /dev/null -w "port $p: %{http_code}\n" --max-time 3 \
       -X POST http://127.0.0.1:$p/mcp \
       -H "Accept: application/json, text/event-stream" \
       -H "Content-Type: application/json" \
       -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
            "params":{"protocolVersion":"2025-03-26","capabilities":{},
                       "clientInfo":{"name":"probe","version":"1"}}}'
   done
   ```
   Set `GPTADMIN_BROWSERCLAW_PORT` to whichever port answers 200.

4. **The saved Admin GPT** must already have an OAuth Action configured
   (run the `--admin-save` command once first; it is idempotent).

---

## Troubleshooting

| Symptom in receipt | Likely cause | Fix |
|---|---|---|
| `call_delta: 0` and `call_200_observed: false` | ChatGPT still "thinking" when the runner exited | Increase `--post-consent-wait` (try 240-300) |
| `call_delta: 0` but `discover_delta > 0` | OAuth scope returned `gptadmin.read` only; uptime needs `gptadmin.exec` | Re-run `--admin-save` with `--admin-scope "gptadmin.read gptadmin.exec"` |
| `oauth_consent_required` but no `auto_clicked_oauth_consent` | The runner was killed mid-consent | Increase `--consent-timeout` |
| `extra_consent_clicks_total: 0` | ChatGPT finished before the runner's polling loop noticed | Reduce `--consent-timeout` if you need faster exit |
| `delta_mcp_relay_calls: "skipped (sudo -n unavailable)" | sudo access to nginx log missing | `sudo -v` once before launching |

---

## What the runner will NEVER do

* Log Authorization headers, bearer tokens, cookies, or chat content.
* Auto-paste a bearer token (operator-only).
* Click outside of: tab open, type prompt, click auth gates ("Войти в систему",
  "Разрешить" / "Allow"), and snapshot.
* Modify the saved GPT itself (use `--admin-save` for that).
* Open or read tabs owned by other agents or by the operator personally.
