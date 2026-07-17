#!/usr/bin/env bash
set -euo pipefail

# GPTAdmin ShellMCP Android/Termux installer.
# Installs only the ShellMCP reliable transport; hub stays on your server.
#
# Usage in Termux:
#   pkg install -y curl tar
#   curl -fsSL https://became.bezrabotnyi.com/install_android.sh | bash
#
# Env overrides:
#   PACKAGE_URL, GPTADMIN_DIR, HUB_URL, SHELLMCP_NAME, SHELLMCP_TOKEN,
#   SHELLMCP_PORT, SHELLMCP_AUTO_START, SHELLMCP_FOREGROUND, SHELLMCP_QUEUE_TIMEOUT_S
#   SHELLMCP_ANDROID_PRIVILEGE=auto|none|shizuku|shizuku-all, SHELLMCP_SHIZUKU_RISH=/path/to/rish,
#   SHELLMCP_UPDATE_INTERVAL_S

PACKAGE_URL=${PACKAGE_URL:-https://github.com/megamen32/gptadmin_opensource/releases/latest/download/gptadmin-android-arm64.tar.gz}
GPTADMIN_DIR=${GPTADMIN_DIR:-$HOME/.local/share/gptadmin}
CONFIG_DIR=${GPTADMIN_CONFIG_DIR:-$HOME/.config/gptadmin}
BIN_DIR=${BIN_DIR:-$PREFIX/bin}
SERVICE_NAME=${SHELLMCP_SERVICE_NAME:-gptadmin-shellmcp}
SERVICE_DIR=${SERVICE_DIR:-${PREFIX:-/data/data/com.termux/files/usr}/var/service/$SERVICE_NAME}
LOG_DIR=${LOG_DIR:-$GPTADMIN_DIR/logs}
SPOOL_DIR=${SHELLMCP_SPOOL_DIR:-$GPTADMIN_DIR/spool}
IDENTITY_DIR=${SHELLMCP_IDENTITY_DIR:-$CONFIG_DIR}
ENV_FILE=${ENV_FILE:-$CONFIG_DIR/shellmcp.env}
RUN_FILE=${RUN_FILE:-$GPTADMIN_DIR/run-shellmcp.sh}
TARGET_BIN=${TARGET_BIN:-$BIN_DIR/gptadmin-shellmcp}
HUB_URL=${HUB_URL:-}
SHELLMCP_PORT=${SHELLMCP_PORT:-25900}
SHELLMCP_QUEUE_TIMEOUT_S=${SHELLMCP_QUEUE_TIMEOUT_S:-55}
SHELLMCP_UPDATE_INTERVAL_S=${SHELLMCP_UPDATE_INTERVAL_S:-3600}
SHELLMCP_AUTO_START=${SHELLMCP_AUTO_START:-1}
SHELLMCP_FOREGROUND=${SHELLMCP_FOREGROUND:-0}
SHELLMCP_ANDROID_PRIVILEGE=${SHELLMCP_ANDROID_PRIVILEGE:-auto}
SHELLMCP_SHIZUKU_RISH=${SHELLMCP_SHIZUKU_RISH:-${SHIZUKU_RISH:-$BIN_DIR/rish}}
RISH_PRESERVE_ENV=${RISH_PRESERVE_ENV:-0}

if [[ -z "${PREFIX:-}" || ! -d "${PREFIX:-/nope}" ]]; then
  echo "ERROR: this installer is meant to run inside Termux. PREFIX is missing." >&2
  exit 2
fi

need() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1. Try: pkg install -y $1" >&2; exit 127; }; }
need curl
need tar
need uname

if [[ -z "$HUB_URL" ]]; then
  read -r -p "GPTAdmin Hub URL [https://gptadmin.bezrabotnyi.com]: " HUB_URL || true
  HUB_URL=${HUB_URL:-https://gptadmin.bezrabotnyi.com}
fi
HUB_URL=${HUB_URL%/}

if [[ -z "${SHELLMCP_TOKEN:-}" ]]; then
  if command -v openssl >/dev/null 2>&1; then
    SHELLMCP_TOKEN=$(openssl rand -hex 16)
  else
    SHELLMCP_TOKEN=$(date +%s%N | sha256sum | awk '{print $1}' | cut -c1-32)
  fi
fi

if [[ -z "${SHELLMCP_NAME:-}" ]]; then
  host=$(getprop ro.product.model 2>/dev/null | tr ' /' '--' | tr -cd '[:alnum:]._-' || true)
  serial=$(getprop ro.serialno 2>/dev/null | tr -cd '[:alnum:]' | tail -c 6 || true)
  SHELLMCP_NAME="android-${host:-termux}${serial:+-$serial}"
fi

case "$SHELLMCP_ANDROID_PRIVILEGE" in
  auto|auto-all|none|off|0|false|shizuku|rish|shizuku-all|rish-all|all) ;;
  *)
    echo "ERROR: SHELLMCP_ANDROID_PRIVILEGE must be auto, none, shizuku, or shizuku-all" >&2
    exit 2
    ;;
esac

if [[ "$SHELLMCP_ANDROID_PRIVILEGE" == auto* || "$SHELLMCP_ANDROID_PRIVILEGE" == shizuku* || "$SHELLMCP_ANDROID_PRIVILEGE" == rish* || "$SHELLMCP_ANDROID_PRIVILEGE" == all ]]; then
  if [[ ! -x "$SHELLMCP_SHIZUKU_RISH" ]] && ! command -v rish >/dev/null 2>&1; then
    if [[ "$SHELLMCP_ANDROID_PRIVILEGE" == auto* ]]; then
      echo "INFO: Shizuku/rish not found; keeping normal Termux shell mode. Export rish later and restart to enable auto privilege." >&2
    else
      cat >&2 <<WARN
WARN: Shizuku privilege mode is enabled, but rish was not found.
      In Shizuku app: Use Shizuku in terminal apps -> Export files.
      Then put rish/rish_shizuku.dex in Termux, for example:
        $BIN_DIR/rish
        $BIN_DIR/rish_shizuku.dex
      Or rerun with SHELLMCP_SHIZUKU_RISH=/path/to/rish.
WARN
    fi
  else
    echo "INFO: Shizuku/rish found; Android privilege mode: $SHELLMCP_ANDROID_PRIVILEGE" >&2
  fi
fi

mkdir -p "$GPTADMIN_DIR" "$CONFIG_DIR" "$BIN_DIR" "$LOG_DIR" "$SPOOL_DIR" "$IDENTITY_DIR"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading $PACKAGE_URL"
curl -fsSL "$PACKAGE_URL" -o "$TMP_DIR/gptadmin-android-arm64.tar.gz"
tar -xzf "$TMP_DIR/gptadmin-android-arm64.tar.gz" -C "$TMP_DIR"
if [[ ! -x "$TMP_DIR/bin/shellmcp" ]]; then
  echo "ERROR: package does not contain bin/shellmcp" >&2
  exit 1
fi
install -m 0755 "$TMP_DIR/bin/shellmcp" "$TARGET_BIN"

cat > "$ENV_FILE" <<ENV
SHELLMCP_TOKEN=$SHELLMCP_TOKEN
HUB_URL=$HUB_URL
SHELLMCP_NAME=$SHELLMCP_NAME
SHELLMCP_PORT=$SHELLMCP_PORT
SHELLMCP_HOST=127.0.0.1
SHELLMCP_URL=http://127.0.0.1:$SHELLMCP_PORT
SHELLMCP_MODE=long_poll
SHELLMCP_QUEUE=1
SHELLMCP_HEARTBEAT=${SHELLMCP_HEARTBEAT:-0}
HB_INTERVAL_S=${HB_INTERVAL_S:-3600}
QUEUE_LONG_POLL_TIMEOUT_S=$SHELLMCP_QUEUE_TIMEOUT_S
SHELLMCP_IDENTITY_DIR=$IDENTITY_DIR
SHELLMCP_SPOOL_DIR=$SPOOL_DIR
SHELLMCP_OUTBOX_DIR=$SPOOL_DIR/outbox
SHELLMCP_AUTO_UPDATE=1
SHELLMCP_UPDATE_INTERVAL_S=$SHELLMCP_UPDATE_INTERVAL_S
SHELLMCP_UPDATE_MANIFEST_URL=$HUB_URL/artifacts/shellmcp-android-arm64.json
SHELLMCP_UPDATE_TOKEN=$SHELLMCP_TOKEN
# The runit/nohup supervisor restarts the process after an atomic binary swap.
SHELLMCP_RESTART_CMD='kill -TERM $PPID'
SHELLMCP_DEFAULT_USER=$(id -un 2>/dev/null || echo u0_a)
SHELLMCP_DEFAULT_HOME=$HOME
SHELLMCP_DEFAULT_CWD=$HOME
LOG_LIMIT_B=65536
EXEC_TIMEOUT=300
SHELLMCP_ANDROID_PRIVILEGE=$SHELLMCP_ANDROID_PRIVILEGE
SHELLMCP_SHIZUKU_RISH=$SHELLMCP_SHIZUKU_RISH
RISH_PRESERVE_ENV=$RISH_PRESERVE_ENV
ENV
chmod 600 "$ENV_FILE"

cat > "$RUN_FILE" <<'RUN'
#!/usr/bin/env bash
set -euo pipefail
ENV_FILE=${ENV_FILE:-$HOME/.config/gptadmin/shellmcp.env}
set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a
mkdir -p "${SHELLMCP_SPOOL_DIR:-$HOME/.local/share/gptadmin/spool}" "$HOME/.local/share/gptadmin/logs"
if command -v termux-wake-lock >/dev/null 2>&1; then termux-wake-lock || true; fi
exec gptadmin-shellmcp >>"$HOME/.local/share/gptadmin/logs/shellmcp.log" 2>&1
RUN
chmod 755 "$RUN_FILE"

installed_service=0
if command -v sv-enable >/dev/null 2>&1 && [[ -d "${PREFIX:-}/var/service" ]]; then
  mkdir -p "$SERVICE_DIR"
  cat > "$SERVICE_DIR/run" <<RUN
#!/data/data/com.termux/files/usr/bin/sh
exec $RUN_FILE
RUN
  chmod 755 "$SERVICE_DIR/run"
  if [[ "$SHELLMCP_AUTO_START" == "1" ]]; then
    sv-enable "$SERVICE_NAME" >/dev/null 2>&1 || true
    sv up "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi
  installed_service=1
elif [[ "$SHELLMCP_AUTO_START" == "1" ]]; then
  nohup "$RUN_FILE" >/dev/null 2>&1 &
fi

cat <<EOF
GPTAdmin Android ShellMCP installed.

Name: $SHELLMCP_NAME
Hub:  $HUB_URL
Bin:  $TARGET_BIN
Env:  $ENV_FILE
Log:  $LOG_DIR/shellmcp.log
Mode: long_poll
Privilege: $SHELLMCP_ANDROID_PRIVILEGE
Shizuku rish: $SHELLMCP_SHIZUKU_RISH
Service: $([[ $installed_service == 1 ]] && echo termux-services:$SERVICE_NAME || echo nohup/manual)

Next:
  1) In Android settings, disable battery optimization for Termux.
  2) Optional Shizuku mode:
       - install/start Shizuku
       - Shizuku app -> Use Shizuku in terminal apps -> Export files
       - copy rish and rish_shizuku.dex into Termux, usually $BIN_DIR
       - default mode is auto: root/sudo shell_exec uses rish when rish is present
       - force with: SHELLMCP_ANDROID_PRIVILEGE=shizuku
     Modes: auto=normal Termux plus rish for root/sudo when present, none=normal Termux, shizuku=root/sudo requests via rish, shizuku-all=all shell_exec via rish.
  3) Keep Termux:API installed if you want commands like termux-location later.
  4) Approve pending server in GPTAdmin hub if auto-approval is not configured.

Manual start:
  $RUN_FILE

Status:
  tail -f $LOG_DIR/shellmcp.log
EOF

if [[ "$SHELLMCP_FOREGROUND" == "1" ]]; then
  exec "$RUN_FILE"
fi
