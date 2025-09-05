#!/usr/bin/env bash
set -euo pipefail

CLI_URL=${CLI_URL:-https://became.bezrabotnyi.com/gptadmin.py}
PKG_ALL_URL=${PKG_ALL_URL:-https://became.bezrabotnyi.com/gptadmin.tar.gz}
PKG_HUB_URL=${PKG_HUB_URL:-https://became.bezrabotnyi.com/gptadmin-hub.tar.gz}
PKG_ROOTD_URL=${PKG_ROOTD_URL:-https://became.bezrabotnyi.com/gptadmin-rootd.tar.gz}

INSTALL_DIR="/opt/gptadmin"
CLI_PATH="/usr/local/bin/gptadmin"

err(){ echo "ERROR: $*" >&2; exit 1; }
need_root(){ [ "$(id -u)" -eq 0 ] || err "run as root (sudo)"; }
have(){ command -v "$1" >/dev/null 2>&1; }

need_root
have curl || err "curl required"
have python3 || err "python3 required"

mkdir -p "$INSTALL_DIR"
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

# 2) Интерактивный мастер (правильный stdin)
if [ -t 0 ]; then
  "$CLI_PATH" setup --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-rootd "$PKG_ROOTD_URL" || err "setup failed"
elif [ -r /dev/tty ]; then
  "$CLI_PATH" setup --pkg-all "$PKG_ALL_URL" --pkg-hub "$PKG_HUB_URL" --pkg-rootd "$PKG_ROOTD_URL" < /dev/tty || err "setup failed"
else
  err "no TTY available for interactive setup. Run: bash <(curl -fsSL https://.../install.sh)"
fi

cat <<EOF
\n✅ GPTAdmin CLI установлен: $CLI_PATH
Использование (примеры):
  gptadmin status
  gptadmin tokens           # покажет ТОЛЬКО CTL_TOKEN (хаб)
  gptadmin logs hub         # логи хаба
  gptadmin port 4555        # смена порта хаба

EOF
