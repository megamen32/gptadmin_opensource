#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${TMPDIR:-/tmp}/gptadmin-exec-context-candidate-build"
cache_dir="${GOCACHE:-/tmp/gptadmin-go-cache}"
report_path="${1:-${repo_root}/.agents/exec-context-20261022/e2e-results.json}"
mkdir -p "$out_dir" "$cache_dir"
export GOCACHE="$cache_dir"
export GOMAXPROCS="${GOMAXPROCS:-4}"

build_version=1029
ldflags="-X github.com/megamen32/gptadmin/go-hub/internal/hub.BuildVersion=${build_version} -X github.com/megamen32/gptadmin/go-shellmcp/internal/server.BuildVersion=${build_version}"
(
  cd "$repo_root/go-hub"
  go build -buildvcs=false -ldflags "$ldflags" -o "$out_dir/gptadmin-node" ./cmd/gptadmin-node
  go build -buildvcs=false -o "$out_dir/exec-context-child-mcp" ../scripts/fixtures/exec-context-mcp.go
  go build -buildvcs=false -o "$out_dir/exec-context-root-mcp" ../scripts/fixtures/exec-context-root-mcp.go
)
python3 "$repo_root/scripts/exec_context_e2e.py" \
  "$out_dir/gptadmin-node" \
  "$out_dir/exec-context-child-mcp" \
  "$out_dir/exec-context-root-mcp" \
  --report "$report_path"
