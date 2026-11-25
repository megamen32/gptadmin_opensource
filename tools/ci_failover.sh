#!/usr/bin/env bash
# Run the real scenario while one writer per image refreshes the optional cache.
set -euo pipefail
suite="${1:?suite required}"
source_sha="${GITHUB_SHA:?exact source SHA required}"
[[ "$source_sha" =~ ^[0-9a-f]{40}$ ]] || { echo "Invalid source SHA" >&2; exit 2; }
case "$suite" in
  legacy|member-outage) target=failover-e2e; scope=base ;;
  quorum-outages|quorum-fencing|quorum-storage|quorum-dispatch-capacity) target=quorum-e2e; scope=quorum ;;
  *) echo "Unknown suite: $suite" >&2; exit 2 ;;
esac

cache_pid=""
cache_log="${RUNNER_TEMP:-/tmp}/gptadmin-cache-$suite.log"
cleanup() {
  if [[ -n "$cache_pid" ]]; then
    kill "$cache_pid" 2>/dev/null || true
    wait "$cache_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT
# The six jobs import both image caches. Only these two jobs write, avoiding
# racing exporters for the same scope and overlapping upload with real tests.
if [[ "$suite" == member-outage || "$suite" == quorum-storage ]]; then
  if [[ -n "${ACTIONS_RUNTIME_TOKEN:-}" ]]; then
    docker buildx bake \
      -f tests/e2e/failover/docker-compose.yml \
      -f tests/e2e/node/compose.quorum.yml \
      --set '*.context=.' \
      --set "$target.output=type=cacheonly" \
      --set "$target.cache-to=type=gha,scope=public-failover-$scope-v1,mode=max,timeout=90s,ignore-error=true" \
      "$target" >"$cache_log" 2>&1 &
    cache_pid=$!
  else
    echo "::warning::Docker cache refresh unavailable; running the full scenario"
  fi
fi

set +e
case "$suite" in
  legacy)
    docker run --rm --init "gptadmin-ci-failover:$source_sha"
    ;;
  member-outage)
    docker run --rm --init "gptadmin-ci-failover:$source_sha" \
      python3 /src/tests/e2e/node/member_outage_run.py /usr/local/bin/gptadmin-node \
      --shell-binary /usr/local/bin/gptadmin-shellmcp
    ;;
  quorum-*)
    # Real process-crash/snapshot tests keep their files across all Node restarts.
    docker run --rm --init --network none --cap-add NET_ADMIN \
      --tmpfs /tmp:rw,nosuid,nodev,exec,size=512m \
      --add-host node0.test:127.0.0.1 --add-host node1.test:127.0.0.1 --add-host node2.test:127.0.0.1 \
      "gptadmin-ci-quorum:$source_sha" \
      python3 -B /src/tests/e2e/node/quorum_failover_run.py /usr/local/bin/gptadmin-node /tmp \
      --shell-binary /usr/local/bin/gptadmin-shellmcp --case "${suite#quorum-}"
    ;;
esac
scenario_status=$?
if [[ "$scenario_status" -ne 0 ]]; then exit "$scenario_status"; fi
if [[ -n "$cache_pid" ]]; then
  wait "$cache_pid"
  cache_status=$?
  cache_pid=""
  if [[ "$cache_status" -ne 0 ]]; then
    echo "::warning::Optional Docker cache refresh failed ($cache_status)"
    tail -n 10 "$cache_log" || true
  fi
fi
exit "$scenario_status"
