#!/bin/bash
set -euo pipefail
ACTION=${1:-restart-primary}
STATE=/run/gptadmin-handover.json
UPSTREAM=/etc/nginx/conf.d/gptadmin-hub-upstream.conf
write_state(){ printf '{"status":"%s","action":"%s","ts":%s,"detail":"%s"}\n' "$1" "$ACTION" "$(date +%s)" "${2:-}" > "$STATE"; }
health(){ curl -fsS --max-time 2 "$1/healthz" >/dev/null; }
write_upstream(){
  local primary=$1 backup=$2 tmp
  tmp=$(mktemp /etc/nginx/conf.d/.gptadmin-hub-upstream.XXXXXX)
  cat >"$tmp" <<CFG
upstream gptadmin_hub_active {
    zone gptadmin_hub_active 64k;
    server 127.0.0.1:${primary} max_fails=1 fail_timeout=1s;
    server 127.0.0.1:${backup} backup;
    keepalive 64;
}
CFG
  chmod 0644 "$tmp"
  mv "$tmp" "$UPSTREAM"
  nginx -t
  systemctl reload nginx
}
case "$ACTION" in
 restart-primary)
  # Standby and primary share durable state files but have separate in-memory
  # snapshots. Refresh standby immediately before promotion so it starts from
  # the latest registry/task state rather than overwriting it with stale data.
  write_state running 'refreshing standby from durable state'
  systemctl restart gptadmin-hub-standby.service
  for i in $(seq 1 100); do health http://127.0.0.1:19001 && break; sleep .1; done
  health http://127.0.0.1:19001 || { write_state failed 'standby unhealthy after refresh'; exit 1; }
  write_state running 'standby refreshed; promoting'
  write_upstream 19001 9001

  # Drain pre-existing long-poll/keep-alive requests from primary before it is
  # restarted. New traffic is already on standby.
  sleep 65
  write_state running 'standby active; restarting primary'
  systemctl restart gptadmin-hub.service
  for i in $(seq 1 100); do health http://127.0.0.1:9001 && break; sleep .1; done
  health http://127.0.0.1:9001 || { write_state failed 'primary unhealthy after restart'; exit 1; }
  write_upstream 9001 19001

  # Do not restart standby here: requests accepted while it was active may
  # still be draining. It is refreshed before the next promotion instead.
  write_state completed 'primary healthy and active; standby will refresh before next promotion'
  ;;
 *) write_state failed 'unknown action'; exit 2;;
esac
