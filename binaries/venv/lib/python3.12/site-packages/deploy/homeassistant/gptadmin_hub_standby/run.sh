#!/usr/bin/env bash
set -euo pipefail

OPTS=/data/options.json
jqr(){ jq -r "$1 // empty" "$OPTS"; }

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
export MCP_RELAY_AGENT_TOKEN="$(jqr '.mcp_relay_agent_token')"
export SHELLMCP_TOKEN="$(jqr '.shellmcp_token')"
export SHELL_TOKEN="$SHELLMCP_TOKEN"
export OAUTH_CLIENT_SECRET="$(jqr '.oauth_client_secret')"
export ADMIN_PASSWORD="$(jqr '.admin_password')"
export MCP_BRIDGE_KEY="$(jqr '.mcp_bridge_key')"
export PUBLIC_ORIGIN="$(jqr '.public_origin')"
export MCP_RESOURCE="$(jqr '.mcp_resource')"
export HUB_PUBLIC_URL="$(jqr '.hub_public_url')"
export HUB_URL="$(jqr '.hub_url')"
export OAUTH_PERMISSIVE_REDIRECTS="$(jqr '.oauth_permissive_redirects')"
export OAUTH_PERMISSIVE_RESOURCES="$(jqr '.oauth_permissive_resources')"
export MCP_RELAY_DEFAULT_TIMEOUT="$(jqr '.mcp_relay_default_timeout')"
export MCP_RELAY_POLL_MAX_TIMEOUT="$(jqr '.mcp_relay_poll_max_timeout')"
export AUTH_LOG_SECRETS=0

: "${GPTADMIN_HUB_HOST:=0.0.0.0}"
: "${GPTADMIN_HUB_PORT:=9001}"
: "${MCP_RELAY_DEFAULT_TIMEOUT:=30}"
: "${MCP_RELAY_POLL_MAX_TIMEOUT:=55}"

mkdir -p /data/config /data/outputs /opt/gptadmin/build /opt/gptadmin/public
exec /usr/local/bin/gptadmin_hub
