#!/bin/bash
set -euo pipefail
pid=${1:-0}
UPSTREAM=/etc/nginx/conf.d/gptadmin-hub-upstream.conf
PRIMARY=9001
STANDBY=19001
log(){ logger -t gptadmin-standby-stop -- "$*"; echo "$*"; }
# If standby is currently serving and primary is healthy, move traffic back
# before terminating standby. Planned handover owns topology itself.
if ! systemctl is-active --quiet gptadmin-handover@restart-primary.service \
   && grep -q "server 127.0.0.1:${STANDBY} max_fails" "$UPSTREAM" \
   && curl -fsS --max-time 1 "http://127.0.0.1:${PRIMARY}/healthz" >/dev/null; then
  tmp=$(mktemp /etc/nginx/conf.d/.gptadmin-hub-upstream.XXXXXX)
  cat >"$tmp" <<CFG
upstream gptadmin_hub_active {
    zone gptadmin_hub_active 64k;
    server 127.0.0.1:${PRIMARY} max_fails=1 fail_timeout=1s;
    server 127.0.0.1:${STANDBY} backup;
    keepalive 64;
}
CFG
  chmod 0644 "$tmp"; mv "$tmp" "$UPSTREAM"; nginx -t >/dev/null; systemctl reload nginx
  log 'failed back to primary synchronously before standby stop'
fi
if [ "$pid" != 0 ] && kill -0 "$pid" 2>/dev/null; then
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || exit 0; sleep .1; done
  kill -KILL "$pid" 2>/dev/null || true
fi
exit 0
