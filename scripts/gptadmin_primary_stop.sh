#!/bin/bash
set -euo pipefail
pid=${1:-0}
log(){ logger -t gptadmin-primary-stop -- "$*"; echo "$*"; }
# Before terminating primary, move new traffic to healthy standby unless a
# planned handover already did so.
if ! systemctl is-active --quiet gptadmin-handover@restart-primary.service; then
  /usr/local/sbin/gptadmin-primary-down-switch || true
fi
# Now terminate the primary process. Keep stop bounded so systemd never waits
# on long-poll connections indefinitely.
if [ "$pid" != 0 ] && kill -0 "$pid" 2>/dev/null; then
  log "terminating primary pid=$pid after traffic switch"
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do
    kill -0 "$pid" 2>/dev/null || exit 0
    sleep .1
  done
  log "primary pid=$pid did not stop in 5s; killing"
  kill -KILL "$pid" 2>/dev/null || true
fi
exit 0
