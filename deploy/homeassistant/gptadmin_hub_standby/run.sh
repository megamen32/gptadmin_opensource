#!/usr/bin/env bash
set -euo pipefail

OPTS=/data/options.json
jqr(){ jq -r "$1 // empty" "$OPTS"; }

mkdir -p /data/config /data/outputs /opt/gptadmin/build /opt/gptadmin/public
INTERNAL_SECRETS=/data/config/internal-secrets.json
python3 /usr/local/bin/gptadmin_failover_config.py --ensure-internal-secrets "$INTERNAL_SECRETS"
internal(){ jq -r --arg key "$1" '.[$key] // empty' "$INTERNAL_SECRETS"; }

export GPTADMIN_ROOT=/opt/gptadmin
export GPTADMIN_PUBLIC_DIR=/opt/gptadmin/public
export GPTADMIN_ARTIFACT_DIR=/opt/gptadmin/build
export GPTADMIN_CONFIG_DIR=/data/config
export GPTADMIN_OUTPUT_DIR=/data/outputs
export GPTADMIN_HUB_HOST="$(jqr '.hub_host')"
export GPTADMIN_HUB_PORT="$(jqr '.hub_port')"
export HUB_HOST="$GPTADMIN_HUB_HOST"
export HUB_PORT="$GPTADMIN_HUB_PORT"
export HUB_BIND="$GPTADMIN_HUB_HOST"
export CTL_TOKEN="$(jqr '.ctl_token')"
: "${CTL_TOKEN:=$(internal ctl_token)}"
export MCP_RELAY_AGENT_TOKEN="$(jqr '.mcp_relay_agent_token')"
: "${MCP_RELAY_AGENT_TOKEN:=$(internal mcp_relay_agent_token)}"
export SHELLMCP_TOKEN="$(jqr '.shellmcp_token')"
: "${SHELLMCP_TOKEN:=$(internal shellmcp_token)}"
export SHELL_TOKEN="$SHELLMCP_TOKEN"
export OAUTH_CLIENT_SECRET="$(jqr '.oauth_client_secret')"
: "${OAUTH_CLIENT_SECRET:=$(internal oauth_client_secret)}"
export ADMIN_PASSWORD="$(jqr '.admin_password')"
export MCP_BRIDGE_KEY="$(jqr '.mcp_bridge_key')"
: "${MCP_BRIDGE_KEY:=$(internal mcp_bridge_key)}"
export GPTADMIN_CODEX_MCP_BEARER="$(jqr '.codex_mcp_bearer')"
export GPTADMIN_CLAUDE_MCP_BEARER="$(jqr '.claude_mcp_bearer')"
export GPTADMIN_CUSTOM_MCP_BEARER="$(jqr '.custom_mcp_bearer')"
export GPTADMIN_OPENCODE_MCP_BEARER="$(jqr '.opencode_mcp_bearer')"
export GPTADMIN_HERMES_MCP_BEARER="$(jqr '.hermes_mcp_bearer')"
export GPTADMIN_OPENCLAW_MCP_BEARER="$(jqr '.openclaw_mcp_bearer')"
export GPTADMIN_VSCODE_MCP_BEARER="$(jqr '.vscode_mcp_bearer')"
export GPTADMIN_ZED_MCP_BEARER="$(jqr '.zed_mcp_bearer')"
export GPTADMIN_AVAILABILITY_MONITOR_MCP_BEARER="$(jqr '.availability_monitor_mcp_bearer')"
export PUBLIC_ORIGIN="$(jqr '.public_origin')"
export MCP_RESOURCE="$(jqr '.mcp_resource')"
export HUB_PUBLIC_URL="$(jqr '.hub_public_url')"
export HUB_URL="$(jqr '.hub_url')"
export OAUTH_PERMISSIVE_REDIRECTS="$(jqr '.oauth_permissive_redirects')"
export OAUTH_PERMISSIVE_RESOURCES="$(jqr '.oauth_permissive_resources')"
export MCP_RELAY_DEFAULT_TIMEOUT="$(jqr '.mcp_relay_default_timeout')"
export MCP_RELAY_POLL_MAX_TIMEOUT="$(jqr '.mcp_relay_poll_max_timeout')"
export FRP_TOKEN="$(jqr '.failover_frp_token')"
export AUTH_LOG_SECRETS=0

: "${GPTADMIN_HUB_HOST:=0.0.0.0}"
: "${GPTADMIN_HUB_PORT:=9001}"
: "${MCP_RELAY_DEFAULT_TIMEOUT:=30}"
: "${MCP_RELAY_POLL_MAX_TIMEOUT:=55}"
: "${FRP_TOKEN:=}"
: "${MCP_RESOURCE:=$PUBLIC_ORIGIN}"
: "${HUB_PUBLIC_URL:=$PUBLIC_ORIGIN}"
: "${HUB_URL:=$PUBLIC_ORIGIN}"

if jq -e '.failover | type == "object"' "$OPTS" >/dev/null 2>&1; then
  mkdir -p /opt/gptadmin/failover
  python3 /usr/local/bin/gptadmin_failover_config.py \
    --options "$OPTS" \
    --config /opt/gptadmin/failover/failover_config.json \
    --state /opt/gptadmin/failover/failover_state.json
fi

/usr/local/bin/gptadmin_hub &
hub_pid=$!
runtime_pid=""
cleanup() {
  if [[ -n "$runtime_pid" ]]; then
    kill "$runtime_pid" 2>/dev/null || true
    wait "$runtime_pid" 2>/dev/null || true
  fi
  kill "$hub_pid" 2>/dev/null || true
  wait "$hub_pid" 2>/dev/null || true
}
trap cleanup EXIT TERM INT

/usr/local/bin/gptadmin_failover_runtime.py &
runtime_pid=$!
while kill -0 "$hub_pid" 2>/dev/null && kill -0 "$runtime_pid" 2>/dev/null; do
  sleep 2
done
if ! kill -0 "$hub_pid" 2>/dev/null; then
  kill "$runtime_pid" 2>/dev/null || true
fi
wait "$runtime_pid"
