#!/bin/sh
# Supply the existing reader repair service with the address reached through its
# public HTTPS origin. Preserve the recorded route if a probe cannot complete.
set -eu

origin=${GPTADMIN_WRITER_ORIGIN:?configure the writer HTTPS origin}
reader_env=${GPTADMIN_READER_ENV_FILE:-/etc/gptadmin/nodes/primary.env}
if response=$(curl --disable --ipv4 --noproxy '*' --proto '=https' --silent --show-error \
    --max-time 5 --output /dev/null --write-out '%{http_code} %{remote_ip}' "${origin%/}/healthz") &&
    [ "${response%% *}" = 200 ]; then
    GPTADMIN_AUTOHEAL_WRITER_IP=${response#* }
else
    # The installed manager writes this setting as a quoted IPv4 literal.
    # It still checks and repairs the local reader while the origin is unavailable.
    GPTADMIN_AUTOHEAL_WRITER_IP=$(sed -nE 's/^[[:space:]]*GPTADMIN_NODE_WRITER_CONNECT_TO="?([0-9.]+)"?[[:space:]]*$/\1/p' "$reader_env")
    if [ -z "$GPTADMIN_AUTOHEAL_WRITER_IP" ]; then
        echo 'No verified writer address or saved reader route' >&2
        exit 1
    fi
fi
export GPTADMIN_AUTOHEAL_WRITER_IP
exec "$@"
