#!/usr/bin/env bash
set -euo pipefail

# Packages live on GitHub Releases (canonical, versioned); the install
# bootstrap script (gptadmin.py) is still served from the legacy host.
RELEASES_URL=${RELEASES_URL:-https://github.com/megamen32/gptadmin_opensource/releases/latest/download}
BASE_URL=${BASE_URL:-https://became.bezrabotnyi.com}
CLI_URL=${CLI_URL:-$BASE_URL/gptadmin.py}
PKG_FALLBACK_URL=${PKG_FALLBACK_URL:-$RELEASES_URL/gptadmin.tar.gz}
PKG_HUB_URL=${PKG_HUB_URL:-$RELEASES_URL/gptadmin-hub.tar.gz}
PKG_SHELLMCP_URL=${PKG_SHELLMCP_URL:-$RELEASES_URL/gptadmin-shellmcp.tar.gz}

_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$_os" in
  darwin) GPTADMIN_PLATFORM=darwin ;;
  linux) GPTADMIN_PLATFORM=linux ;;
  *) GPTADMIN_PLATFORM="$_os" ;;
esac
_arch="$(uname -m)"
case "$_arch" in
  arm64|aarch64) GPTADMIN_ARCH=arm64 ;;
  x86_64|amd64) GPTADMIN_ARCH=amd64 ;;
  *) GPTADMIN_ARCH="$_arch" ;;
esac
PKG_ALL_URL=${PKG_ALL_URL:-$RELEASES_URL/gptadmin-${GPTADMIN_PLATFORM}-${GPTADMIN_ARCH}.tar.gz}

err(){ echo "ERROR: $*" >&2; exit 1; }
have(){ command -v "$1" >/dev/null 2>&1; }

download_file(){
  local url="$1" dest="$2"
  if [ "${GPTADMIN_DOWNLOAD_QUIET:-}" = "1" ]; then
    curl -fsSL "$url" -o "$dest"
  else
    echo "  URL: $url"
    curl -fL "$url" -o "$dest"
    if [ -f "$dest" ]; then
      python3 - "$dest" <<'PY_SIZE'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
print(f"  Готово: {p.stat().st_size / (1024 * 1024):.1f} MiB -> {p}")
PY_SIZE
    fi
  fi
}

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

CONFIG_DIR="${GPTADMIN_CONFIG_DIR:-}"
if [ -z "$CONFIG_DIR" ]; then
  if [ "$INSTALL_MODE" = "system" ]; then
    CONFIG_DIR="/etc/gptadmin"
  else
    CONFIG_DIR="$USER_HOME/.config/gptadmin"
  fi
fi
ENV_FILE="$CONFIG_DIR/gptadmin.env"
EXISTING_INSTALL=0
if [ -f "$ENV_FILE" ] || [ -x "$INSTALL_DIR/bin/gptadmin_hub" ] || [ -x "$INSTALL_DIR/bin/shellmcp" ]; then
  EXISTING_INSTALL=1
fi

have curl || err "curl required"
have python3 || err "python3 required"

# When installed via: curl -fsSL .../install.sh | sudo bash
# preserve the original invoking user's UID for ShellMCP's default non-root exec mode.
if [ -n "${SUDO_UID:-}" ] && [ "${SUDO_UID:-0}" != "0" ]; then
  export SHELLMCP_DEFAULT_UID="${SHELLMCP_DEFAULT_UID:-$SUDO_UID}"
fi

if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
  export SHELLMCP_DEFAULT_USER="${SHELLMCP_DEFAULT_USER:-$SUDO_USER}"
  if command -v getent >/dev/null 2>&1; then
    _home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
  else
    _home="$(eval echo ~"$SUDO_USER")"
  fi
  export SHELLMCP_DEFAULT_HOME="${SHELLMCP_DEFAULT_HOME:-$_home}"
fi

mkdir -p "$INSTALL_DIR" "$(dirname "$CLI_PATH")"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# 1) CLI
if download_file "$CLI_URL" "$TMP_DIR/gptadmin.py"; then
  echo "[1/2] Downloaded Python CLI"
else
  echo "[1/2] CLI not found at $CLI_URL — fallback to package"
  download_file "$PKG_ALL_URL" "$TMP_DIR/pkg.tar.gz" || download_file "$PKG_FALLBACK_URL" "$TMP_DIR/pkg.tar.gz"
  mkdir -p "$TMP_DIR/pkg" && tar -xzf "$TMP_DIR/pkg.tar.gz" -C "$TMP_DIR/pkg"
  [ -f "$TMP_DIR/pkg/cli/gptadmin.py" ] || err "cli/gptadmin.py not found in package"
  cp "$TMP_DIR/pkg/cli/gptadmin.py" "$TMP_DIR/gptadmin.py"
fi
install -m 0755 "$TMP_DIR/gptadmin.py" "$CLI_PATH"
export GPTADMIN_HOME="$INSTALL_DIR"

# 2) Existing install -> update by default. Fresh install -> interactive setup.
ACTION="${GPTADMIN_INSTALL_ACTION:-}"
if [ -z "$ACTION" ] && [ "$EXISTING_INSTALL" = "1" ]; then
  ACTION="update"
  if [ -r /dev/tty ]; then
    echo
    echo "Найдена существующая установка GPTAdmin: $ENV_FILE"
    echo "  1) Обновить существующую установку in-place (по умолчанию)"
    echo "  2) Запустить полный setup заново"
    printf "Ваш выбор [1]: " > /dev/tty
    read -r _choice < /dev/tty || _choice=""
    case "${_choice:-1}" in
      2|setup|Setup|SETUP) ACTION="setup" ;;
      *) ACTION="update" ;;
    esac
  fi
fi
if [ -z "$ACTION" ]; then
  ACTION="setup"
fi

if [ "$ACTION" = "update" ]; then
  "$CLI_PATH" update --$INSTALL_MODE --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-shellmcp "$PKG_SHELLMCP_URL" || err "update failed"
elif [ "$ACTION" = "setup" ]; then
  if [ -t 0 ]; then
    "$CLI_PATH" setup --$INSTALL_MODE --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-shellmcp "$PKG_SHELLMCP_URL" || err "setup failed"
  elif [ -r /dev/tty ]; then
    "$CLI_PATH" setup --$INSTALL_MODE --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-shellmcp "$PKG_SHELLMCP_URL" < /dev/tty || err "setup failed"
  else
    err "no TTY available for interactive setup. Run: bash <(curl -fsSL https://.../install.sh)"
  fi
else
  err "unknown GPTADMIN_INSTALL_ACTION=$ACTION (use update or setup)"
fi

cat <<EOF
\n✅ GPTAdmin CLI установлен: $CLI_PATH
Режим установки: $INSTALL_MODE
Пакет: $PKG_ALL_URL
Использование (примеры):
  gptadmin update           # обновить существующую установку
  gptadmin status
  gptadmin tokens           # покажет ТОЛЬКО CTL_TOKEN (хаб)
  gptadmin logs hub         # логи хаба
  gptadmin port 4555        # смена порта хаба
  gptadmin config shellmcp    # настроить ShellMCP transport
  gptadmin logs shellmcp      # логи ShellMCP
  gptadmin uninstall        # Полное удаление всего с компьютера

EOF
