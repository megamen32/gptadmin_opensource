#!/usr/bin/env bash
set -euo pipefail
mkdir -p /e2e/out
chmod 0777 /e2e/out
: > /e2e/out/summary.log

run() {
  local name="$1"; shift
  echo "=== $name ===" | tee -a /e2e/out/summary.log
  "$@" 2>&1 | tee "/e2e/out/${name}.log"
}

run user-public-hub-shellmcp /e2e/scenarios/user-public-hub-shellmcp.sh
run sudo-frp-shellmcp /e2e/scenarios/sudo-frp-shellmcp.sh
run tunnel-backends-shellmcp /e2e/scenarios/tunnel-backends-shellmcp.sh

echo 'ALL SHELLMCP E2E SCENARIOS PASSED' | tee -a /e2e/out/summary.log
