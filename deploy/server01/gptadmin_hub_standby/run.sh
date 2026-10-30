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
for client in CODEX CLAUDE CUSTOM OPENCODE HERMES OPENCLAW VSCODE ZED AVAILABILITY_MONITOR; do
  bearer_var="GPTADMIN_${client}_MCP_BEARER"
  if [[ -z "${!bearer_var}" ]]; then
    printf -v "$bearer_var" '%s' "$(internal "${client,,}_mcp_bearer")"
    export "$bearer_var"
  fi
done
export PUBLIC_ORIGIN="$(jqr '.public_origin')"
export GPTADMIN_LEGACY_PUBLIC_ORIGINS="$(jqr '(.legacy_public_origins // []) | join(",")')"
export GPTADMIN_OAUTH_ALLOWED_REDIRECTS="$(jq -c '.oauth_allowed_redirects // []' "$OPTS")"
export MCP_RESOURCE="$(jqr '.mcp_resource')"
export HUB_PUBLIC_URL="$(jqr '.hub_public_url')"
export HUB_URL="$(jqr '.hub_url')"
export OAUTH_PERMISSIVE_REDIRECTS="$(jqr '.oauth_permissive_redirects')"
export OAUTH_PERMISSIVE_RESOURCES="$(jqr '.oauth_permissive_resources')"
export GPTADMIN_RELAX_AUTH_CHECKS="$(jqr '.relax_auth_checks')"
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

NODE_MODE="$(jqr '.node_mode')"
: "${NODE_MODE:=legacy}"
case "$NODE_MODE" in
  legacy)
    if [[ "$(jq -r '.quorum_enabled // false' "$OPTS")" == true ]]; then
      echo 'quorum_enabled requires node_mode member' >&2
      exit 1
    fi
    NODE_BINARY=/usr/local/bin/gptadmin_hub
    ;;
  member|writer)
    NODE_BINARY=/usr/local/bin/gptadmin-node
    export SHELL_NAME=haos
    if [[ "$NODE_MODE" == writer ]]; then
      if [[ "$(jq -r '.quorum_enabled // false' "$OPTS")" == true ]]; then
        echo 'single writer mode cannot enable quorum' >&2
        exit 1
      fi
      python3 /usr/local/bin/gptadmin_single_writer.py /data/config /data/identity/shellmcp_identity.json "$PUBLIC_ORIGIN"
      export GPTADMIN_AUTH_MODE=writer
      export GPTADMIN_NODE_PUBLIC_URL="$PUBLIC_ORIGIN"
      unset GPTADMIN_NODE_IDENTITY GPTADMIN_NODE_SEED GPTADMIN_AUTH_SOURCE_ID
    else
      export GPTADMIN_NODE_IDENTITY=/data/config/membership/node-identity.json
      export GPTADMIN_NODE_SEED="$(jqr '.failover.primary_public_url')"
      export GPTADMIN_AUTH_MODE=reader
      export GPTADMIN_READER_OFFLINE_SHARED_SIGNER="$(jqr '.offline_shared_signer')"
    fi
    if [[ "$NODE_MODE" == member && "$(jq -r '.quorum_enabled // false' "$OPTS")" == true ]]; then
      export GPTADMIN_QUORUM_CONFIG_FILE=/data/config/quorum/quorum.json
      GPTADMIN_NODE_PUBLIC_URL="$(python3 /usr/local/bin/gptadmin_quorum_config.py "$GPTADMIN_QUORUM_CONFIG_FILE")"
      export GPTADMIN_NODE_PUBLIC_URL
    fi
    mkdir -p /data/config/membership /data/identity /data/spool /data/outbox
    if [[ "$NODE_MODE" == member && ( ! -s "$GPTADMIN_NODE_IDENTITY" || -z "$GPTADMIN_NODE_SEED" ) ]]; then
      echo 'member mode requires a joined Node identity and failover.primary_public_url as writer seed' >&2
      exit 1
    fi
    if [[ ! -s /data/identity/shellmcp_identity.json || ! -s /data/identity/shellmcp_ed25519 ]]; then
      echo 'member mode requires the existing HAOS Shell identity to preserve its target' >&2
      exit 1
    fi
    export SHELL_IDENTITY_DIR=/data/identity
    export SHELL_SPOOL_DIR=/data/spool
    export SHELL_OUTBOX_DIR=/data/outbox
    export SHELL_DEFAULT_HOME=/config
    export SHELL_DEFAULT_CWD=/config
    export SHELL_DEFAULT_USER=root
    export SHELL_HOST=127.0.0.1
    export SHELL_PORT=25901
    export SHELL_HEARTBEAT=1
    export SHELLMCP_MCP_CONFIG=/data/shellmcp-mcp.json
    if [[ "$NODE_MODE" == member ]]; then
      export GPTADMIN_AUTH_SOURCE_ID="$(jq -r '.writer_id // empty' "$GPTADMIN_NODE_IDENTITY")"
      python3 - "$GPTADMIN_NODE_IDENTITY" <<'PY_NODE_IDENTITY'
import json, re, sys
with open(sys.argv[1], encoding='utf-8') as handle:
    identity = json.load(handle)
if (identity.get('schema') != 'gptadmin.node-identity/v1'
        or not re.fullmatch(r'[0-9a-f]{16,64}', str(identity.get('node_id', '')))
        or not re.fullmatch(r'(?:[0-9a-f]{2}){32}', str(identity.get('node_key', '')))
        or not identity.get('writer_id') or not identity.get('writer_url')):
    raise SystemExit('member mode requires a valid joined Node identity with pinned writer')
PY_NODE_IDENTITY
    fi
    ;;
  *)
    echo 'node_mode must be legacy, member or writer' >&2
    exit 2
    ;;
esac

"$NODE_BINARY" &
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
