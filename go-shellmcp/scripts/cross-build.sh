#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=${CGO_ENABLED:-0}
targets=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
  windows/amd64
)
mkdir -p .build/cross
for target in "${targets[@]}"; do
  GOOS=${target%/*}
  GOARCH=${target#*/}
  ext=""
  [ "$GOOS" = windows ] && ext=".exe"
  out=".build/cross/shellmcp-go-${GOOS}-${GOARCH}${ext}"
  echo "==> $GOOS/$GOARCH -> $out"
  GOOS=$GOOS GOARCH=$GOARCH go build -o "$out" ./cmd/shellmcp-go
  ls -lh "$out"
done
