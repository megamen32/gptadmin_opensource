#!/usr/bin/env bash
# Live external-endpoint canary for the running GPTAdmin hub.
#
# Checks the real product contract on public URLs:
#   1. /actions/openapi.yaml advertises the host it was fetched from
#   2. /secret-input/ serves the one-time secret page
#   3. bearer-authenticated discovery works
#   4. shell execute round-trip returns output
#
# Reads PUBLIC_ORIGIN, TENANT_ORIGIN, GPTADMIN_CUSTOM_MCP_BEARER and
# GPTADMIN_CANARY_TARGET from the system env file (root-only). Every failed
# check is delivered to the owner's Telegram through NoticePlace
# (notify.event.v1 via /v1/events); alerting outages never mask the canary
# result. A non-zero exit leaves the systemd unit failed so admin-health-check
# and `systemctl --failed` surface it too.
set -uo pipefail

ENV_FILE="${GPTADMIN_ENV_FILE:-/etc/gptadmin/gptadmin.env}"
NOTIFY_ENV_FILE="${GPTADMIN_NOTIFY_ENV_FILE:-/etc/gptadmin/notify.env}"

env_val() {
	sudo grep -oP "(?<=^$1=).*" "$ENV_FILE" 2>/dev/null | head -1 | tr -d '"'
}

MAIN_ORIGIN="$(env_val MCP_ORIGIN)"
TENANT_ORIGIN="$(env_val TENANT_ORIGIN)"
BEARER="$(env_val GPTADMIN_CUSTOM_MCP_BEARER)"
TARGET="${GPTADMIN_CANARY_TARGET:-shell:admin-server-100}"

if [ -r "$NOTIFY_ENV_FILE" ]; then
	set -a
	. "$NOTIFY_ENV_FILE"
	set +a
fi

failures=0
failed_names=()
check() {
	local name="$1" want="$2" got="$3"
	if [ "$got" = "$want" ]; then
		echo "PASS $name"
	else
		echo "FAIL $name: want [$want] got [$got]"
		failures=$((failures + 1))
		failed_names+=("$name|$want|$got")
	fi
}

notify_failure() {
	local name="$1" want="$2" got="$3"
	[ -n "${NOTIFY_CENTER_EVENT_URL:-}" ] && [ -n "${NOTIFY_CENTER_TOKEN:-}" ] || return 0
	local slug dedup payload
	slug="$(printf '%s' "$name" | tr -c 'A-Za-z0-9._-' '.')"
	dedup="gptadmin.canary.$slug"
	payload="$(python3 - "$NOTIFY_PROJECT" "$NOTIFY_RECIPIENT" "$dedup" "$name" "$want" "$got" <<'PY'
import json, socket, sys
print(json.dumps({
    "schema": "notify.event.v1",
    "project": sys.argv[1],
    "recipient": sys.argv[2],
    "kind": "incident",
    "severity": "important",
    "title": f"live canary: {sys.argv[4]} failed",
    "body": f"want [{sys.argv[5]}] got [{sys.argv[6]}] on {socket.gethostname()}",
    "dedup_key": sys.argv[3],
}, ensure_ascii=False))
PY
)" || return 0
	curl --noproxy '*' --fail-with-body --silent --max-time 8 \
		-X POST "$NOTIFY_CENTER_EVENT_URL" \
		-H "Authorization: Bearer $NOTIFY_CENTER_TOKEN" \
		-H "Content-Type: application/json" \
		-H "Idempotency-Key: $dedup-$(date +%s)" \
		-d "$payload" > /dev/null 2>&1 || echo "WARN notify delivery failed for $name"
}

spec_servers_url() {
	curl -sS -m 15 "$1/actions/openapi.yaml" 2>/dev/null \
		| grep -m1 -oP '(?<=- url: ")[^"]+'
}

secret_input_status() {
	curl -sS -o /dev/null -w '%{http_code}' -m 15 "$1/secret-input/livecanary" 2>/dev/null
}

discover_status() {
	curl -sS -o /dev/null -w '%{http_code}' -m 20 \
		-H "Authorization: Bearer $BEARER" "$1/mcp-relay/servers" 2>/dev/null
}

execute_stdout() {
	curl -sS -m 60 -X POST -H "Authorization: Bearer $BEARER" \
		-H 'Content-Type: application/json' \
		-d "{\"target\":\"$TARGET\",\"tool_name\":\"shell_exec\",\"cmd\":\"hostname\"}" \
		"$1/mcp-relay/call" 2>/dev/null \
		| grep -oP '(?<="stdout":")[^"]*'
}

for origin in "$MAIN_ORIGIN" "$TENANT_ORIGIN"; do
	[ -n "$origin" ] || continue
	check "spec-domain $origin" "$origin" "$(spec_servers_url "$origin")"
	check "secret-input $origin" "200" "$(secret_input_status "$origin")"
	check "discover $origin" "200" "$(discover_status "$origin")"
	hostname_out="$(execute_stdout "$origin")"
	if [ -n "$hostname_out" ]; then
		echo "PASS execute $origin ($hostname_out)"
	else
		echo "FAIL execute $origin: empty stdout"
		failures=$((failures + 1))
		failed_names+=("execute $origin||empty stdout")
	fi
done

if [ "$failures" -gt 0 ]; then
	for item in "${failed_names[@]}"; do
		IFS='|' read -r name want got <<< "$item"
		notify_failure "$name" "$want" "$got"
	done
	echo "live canary: $failures failure(s); NoticePlace alerted"
	exit 1
fi
echo "live canary: all checks passed"
