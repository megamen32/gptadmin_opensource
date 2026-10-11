#!/usr/bin/env bash
# Install the CloudOS static add-on next to an existing GPTAdmin public tree.
# Run from an extracted gptadmin-cloudos-addon archive:
#   ./install.sh [path-to-cloudos-ui]
set -Eeuo pipefail

ADDON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_DIR="${1:-$ADDON_DIR/cloudos-ui}"
TARGET_PUBLIC_DIR="${GPTADMIN_PUBLIC_DIR:-/opt/gptadmin/public}"

[[ "$TARGET_PUBLIC_DIR" = /* ]] || { echo "ERROR: GPTADMIN_PUBLIC_DIR must be an absolute path" >&2; exit 2; }
[[ -f "$SOURCE_DIR/index.html" ]] || { echo "ERROR: CloudOS add-on is missing $SOURCE_DIR/index.html" >&2; exit 2; }
[[ -d "$TARGET_PUBLIC_DIR" ]] || { echo "ERROR: GPTAdmin public directory does not exist: $TARGET_PUBLIC_DIR" >&2; exit 2; }

stage_dir="$(mktemp -d "$TARGET_PUBLIC_DIR/.cloudos-ui-stage.XXXXXX")"
cleanup() {
  if [[ -n "${stage_dir:-}" && -d "$stage_dir" ]]; then
    rm -rf "$stage_dir"
  fi
}
trap cleanup EXIT
cp -a "$SOURCE_DIR/." "$stage_dir/"

target_dir="$TARGET_PUBLIC_DIR/cloudos-ui"
backup_dir=""
if [[ -e "$target_dir" || -L "$target_dir" ]]; then
  backup_dir="$TARGET_PUBLIC_DIR/.cloudos-ui.previous.$(date -u +%Y%m%dT%H%M%SZ)"
  mv "$target_dir" "$backup_dir"
fi
mv "$stage_dir" "$target_dir"
stage_dir=""

echo "CloudOS installed at $target_dir"
[[ -n "$backup_dir" ]] && echo "Previous CloudOS payload retained at $backup_dir"
echo "Open /cloudos/ in an authenticated admin session; no Hub restart is required."
