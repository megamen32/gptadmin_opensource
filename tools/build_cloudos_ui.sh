#!/usr/bin/env bash
# Build CloudOS as an optional installable add-on.  Standard hub archives
# contain only the lightweight admin UI; install this archive separately when
# the desktop experience is wanted.
set -Eeuo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UI_DIR="$REPO_DIR/cloudos-ui"
ADDON_DIR="$REPO_DIR/build/cloudos-addon"
ADDON_ARCHIVE="$REPO_DIR/build/gptadmin-cloudos-addon.tar.gz"

command -v pnpm >/dev/null || { echo "pnpm is required (packageManager: pnpm@10)" >&2; exit 1; }

cd "$UI_DIR"
if [[ ! -d node_modules ]]; then
  pnpm install --frozen-lockfile
fi
pnpm build

rm -rf "$ADDON_DIR" "$ADDON_ARCHIVE"
mkdir -p "$ADDON_DIR/cloudos-ui"
cp -a "$UI_DIR/dist/." "$ADDON_DIR/cloudos-ui/"
install -m 0755 "$REPO_DIR/deploy/install_cloudos_ui_addon.sh" "$ADDON_DIR/install.sh"
printf '{"name":"gptadmin-cloudos-addon","version":"%s","path":"public/cloudos-ui"}\n' "$(git -C "$REPO_DIR" rev-parse --short HEAD)" > "$ADDON_DIR/manifest.json"
(cd "$ADDON_DIR" && tar -czf "$ADDON_ARCHIVE.tmp.$$" cloudos-ui install.sh manifest.json && mv -f "$ADDON_ARCHIVE.tmp.$$" "$ADDON_ARCHIVE")

echo "CloudOS add-on built:"
echo "  $UI_DIR/dist"
echo "  $ADDON_ARCHIVE"
