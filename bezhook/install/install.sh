#!/bin/sh
# bezhook installer (Linux/macOS). One command:
#   curl -fsSL https://dl.t.became.bezrabotnyi.com/bezhook/install.sh | sh
# Overrides: BEZHOOK_MIRROR, BEZHOOK_INSTALL_DIR
set -eu

MIRROR="${BEZHOOK_MIRROR:-https://dl.t.became.bezrabotnyi.com/bezhook}"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
MACHINE=$(uname -m)
case "$MACHINE" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *)
    echo "bezhook: unsupported architecture: $MACHINE" >&2
    exit 1
    ;;
esac
case "$OS" in
  linux|darwin) ;;
  *)
    echo "bezhook: unsupported OS: $OS (windows: use install.ps1)" >&2
    exit 1
    ;;
esac

BIN_NAME="bezhook-$OS-$ARCH"
DEST_DIR="${BEZHOOK_INSTALL_DIR:-$HOME/.local/bin}"
DEST="$DEST_DIR/bezhook"

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    echo "bezhook: need curl or wget to download" >&2
    exit 1
  fi
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "bezhook: downloading $MIRROR/dl/$BIN_NAME ..."
fetch "$MIRROR/dl/$BIN_NAME" "$TMP/$BIN_NAME"

# Verify checksum when possible.
if fetch "$MIRROR/dl/SHA256SUMS" "$TMP/SHA256SUMS" 2>/dev/null; then
  want=$(grep " $BIN_NAME\$" "$TMP/SHA256SUMS" | awk '{print $1}')
  if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "$TMP/$BIN_NAME" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
      got=$(shasum -a 256 "$TMP/$BIN_NAME" | awk '{print $1}')
    else
      got=""
      echo "bezhook: no sha256 tool found, skipping checksum"
    fi
    if [ -n "$got" ] && [ "$got" != "$want" ]; then
      echo "bezhook: checksum mismatch for $BIN_NAME" >&2
      exit 1
    fi
  fi
fi

mkdir -p "$DEST_DIR"
mv "$TMP/$BIN_NAME" "$DEST"
chmod 0755 "$DEST"

case ":$PATH:" in
  *":$DEST_DIR:"*) ;;
  *) echo "bezhook: note: add $DEST_DIR to PATH" ;;
esac

VERSION=$( ("$DEST" --version) 2>/dev/null || echo unknown)
echo "bezhook: installed $VERSION -> $DEST"
echo
echo "Usage: bezhook <local-port> [--sub name] [--ttl 72h]"
echo "  e.g.  bezhook 3000 --sub myapp"
echo "  ->    https://myapp.t.became.bezrabotnyi.com"
