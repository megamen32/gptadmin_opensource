#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go test ./...
./scripts/cross-build.sh
REQUESTS=${REQUESTS:-120} WORKERS=${WORKERS:-20} ./scripts/stress-local.sh
