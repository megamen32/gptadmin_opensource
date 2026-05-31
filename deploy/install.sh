#!/usr/bin/env bash
set -euo pipefail

CLI_URL=${CLI_URL:-https://became.bezrabotnyi.com/gptadmin.py}
PKG_ALL_URL=${PKG_ALL_URL:-https://became.bezrabotnyi.com/gptadmin.tar.gz}
PKG_HUB_URL=${PKG_HUB_URL:-https://became.bezrabotnyi.com/gptadmin-hub.tar.gz}
PKG_ROOTD_URL=${PKG_ROOTD_URL:-https://became.bezrabotnyi.com/gptadmin-rootd.tar.gz}

err(){ echo "ERROR: $*" >&2; exit 1; }
have(){ command -v "$1" >/dev/null 2>&1; }

if [ "$(id -u)" -eq 0 ] && [ "${GPTADMIN_INSTALL_MODE:-system}" != "user" ]; then
  INSTALL_MODE="system"
  INSTALL_DIR="${GPTADMIN_HOME:-/opt/gptadmin}"
  CLI_PATH="${GPTADMIN_CLI_PATH:-/usr/local/bin/gptadmin}"
else
  INSTALL_MODE="user"
  export GPTADMIN_INSTALL_MODE=user
  if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
    if command -v getent >/dev/null 2>&1; then
      USER_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
    else
      USER_HOME="$(eval echo ~"$SUDO_USER")"
    fi
  else
    USER_HOME="$HOME"
  fi
  INSTALL_DIR="${GPTADMIN_HOME:-$USER_HOME/.local/share/gptadmin}"
  CLI_PATH="${GPTADMIN_CLI_PATH:-$USER_HOME/.local/bin/gptadmin}"
fi

have curl || err "curl required"
have python3 || err "python3 required"

# When installed via: curl -fsSL .../install.sh | sudo bash
# preserve the original invoking user's UID for TermCP's default non-root exec mode.
if [ -n "${SUDO_UID:-}" ] && [ "${SUDO_UID:-0}" != "0" ]; then
  export ROOTD_DEFAULT_UID="${ROOTD_DEFAULT_UID:-$SUDO_UID}"
fi

if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
  export ROOTD_DEFAULT_USER="${ROOTD_DEFAULT_USER:-$SUDO_USER}"
  if command -v getent >/dev/null 2>&1; then
    _home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
  else
    _home="$(eval echo ~"$SUDO_USER")"
  fi
  export ROOTD_DEFAULT_HOME="${ROOTD_DEFAULT_HOME:-$_home}"
fi

mkdir -p "$INSTALL_DIR" "$(dirname "$CLI_PATH")"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# 1) CLI
if curl -fsSL "$CLI_URL" -o "$TMP_DIR/gptadmin.py"; then
  echo "[1/2] Downloaded Python CLI"
else
  echo "[1/2] CLI not found at $CLI_URL — fallback to package"
  curl -fsSL "$PKG_ALL_URL" -o "$TMP_DIR/pkg.tar.gz"
  mkdir -p "$TMP_DIR/pkg" && tar -xzf "$TMP_DIR/pkg.tar.gz" -C "$TMP_DIR/pkg"
  [ -f "$TMP_DIR/pkg/cli/gptadmin.py" ] || err "cli/gptadmin.py not found in package"
  cp "$TMP_DIR/pkg/cli/gptadmin.py" "$TMP_DIR/gptadmin.py"
fi
install -m 0755 "$TMP_DIR/gptadmin.py" "$CLI_PATH"
export GPTADMIN_HOME="$INSTALL_DIR"

# 2) Интерактивный мастер (правильный stdin)
if [ -t 0 ]; then
  "$CLI_PATH" setup --$INSTALL_MODE --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-rootd "$PKG_ROOTD_URL" || err "setup failed"
elif [ -r /dev/tty ]; then
  "$CLI_PATH" setup --$INSTALL_MODE --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-rootd "$PKG_ROOTD_URL" < /dev/tty || err "setup failed"
else
  err "no TTY available for interactive setup. Run: bash <(curl -fsSL https://.../install.sh)"
fi

cat <<EOF
\n✅ GPTAdmin CLI установлен: $CLI_PATH
Режим установки: $INSTALL_MODE
Использование (примеры):
  gptadmin status
  gptadmin tokens           # покажет ТОЛЬКО CTL_TOKEN (хаб)
  gptadmin logs hub         # логи хаба
  gptadmin port 4555        # смена порта хаба
  gptadmin config termcp    # настроить TermCP transport
  gptadmin logs termcp      # логи TermCP
  gptadmin uninstall        # Полное удаление всего с компьютера

EOF
