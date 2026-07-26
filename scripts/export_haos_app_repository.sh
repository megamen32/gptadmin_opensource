#!/usr/bin/env bash
set -euo pipefail

OUTPUT=""
SOURCE_REF=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT=${2:?missing output directory}; shift 2 ;;
    --source-ref) SOURCE_REF=${2:?missing source ref}; shift 2 ;;
    --help|-h)
      sed -n '1,32p' "$0"
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

ROOT=$(git rev-parse --show-toplevel)
SRC="$ROOT/deploy/homeassistant/gptadmin_hub_standby"
OUTPUT=${OUTPUT:?--output is required}
SOURCE_REF=${SOURCE_REF:-$(git -C "$ROOT" rev-parse HEAD)}

[[ "$OUTPUT" != "$ROOT" && "$OUTPUT" != "$ROOT/" ]] || {
  echo "refusing to export over the source repository" >&2
  exit 2
}
if [[ -e "$OUTPUT" && -n "$(find "$OUTPUT" -mindepth 1 -print -quit 2>/dev/null)" ]]; then
  echo "output directory must be empty: $OUTPUT" >&2
  exit 2
fi
mkdir -p "$OUTPUT/gptadmin_hub_standby"

cp "$SRC/public_repository.yaml" "$OUTPUT/repository.yaml"
cp "$SRC/public_README.md" "$OUTPUT/README.md"
cp "$SRC/public_config.yaml" "$OUTPUT/gptadmin_hub_standby/config.yaml"
cp "$SRC/Dockerfile.public" "$OUTPUT/gptadmin_hub_standby/Dockerfile"
cp "$SRC/run.sh" "$OUTPUT/gptadmin_hub_standby/run.sh"
cp "$SRC/public_README.md" "$OUTPUT/gptadmin_hub_standby/README.md"
cp "$SRC/public_DOCS.md" "$OUTPUT/gptadmin_hub_standby/DOCS.md"
cp "$SRC/public_CHANGELOG.md" "$OUTPUT/gptadmin_hub_standby/CHANGELOG.md"
sed -e 's#/etc/gptadmin/failover_config.json#/data/config/failover_config.json#' \
    -e 's#/etc/gptadmin/failover_state.json#/data/config/failover_state.json#' \
    -e 's#/etc/gptadmin/frpc-failover.toml#/data/config/frpc-failover.toml#' \
    "$ROOT/scripts/gptadmin_failover_watchdog.py" > "$OUTPUT/gptadmin_hub_standby/gptadmin_failover_watchdog.py"
cp "$ROOT/scripts/gptadmin_failover_proxy.py" "$OUTPUT/gptadmin_hub_standby/"
cp "$SRC/gptadmin_failover_runtime.py" "$OUTPUT/gptadmin_hub_standby/"
cp "$SRC/gptadmin_failover_config.py" "$OUTPUT/gptadmin_hub_standby/"

cat > "$OUTPUT/SOURCE.json" <<EOF
{
  "source_ref": "$(printf '%s' "$SOURCE_REF" | sed 's/[\\"]//g')",
  "app_version": "1.0.5",
  "architecture": "aarch64",
  "image": "ghcr.io/megamen32/gptadmin-haos-hub-standby"
}
EOF

chmod 0755 "$OUTPUT/gptadmin_hub_standby/run.sh"
if grep -RInE '192\.168\.|/etc/gptadmin|__[A-Z0-9_]+__' "$OUTPUT" >/dev/null; then
  echo "public export contains instance data or generated state" >&2
  exit 3
fi

echo "exported=$OUTPUT"
echo "source_ref=$SOURCE_REF"
