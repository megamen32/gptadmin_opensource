#!/bin/bash
set -euo pipefail
LOCK=/run/gptadmin-primary-down-switch.lock
UPSTREAM=/etc/nginx/conf.d/gptadmin-hub-upstream.conf
STATE=/run/gptadmin-failover-controller.json
PRIMARY=9001
STANDBY=19001
exec 9>"$LOCK"
flock -n 9 || exit 0
log(){ logger -t gptadmin-primary-down-switch -- "$*"; echo "$*"; }
# Planned handover owns topology itself.
if systemctl is-active --quiet gptadmin-handover@restart-primary.service; then log 'skip: manual handover active'; exit 0; fi
# If primary is not the current nginx active, nothing to do.
grep -q "server 127.0.0.1:${PRIMARY} max_fails" "$UPSTREAM" || { log 'skip: primary not active'; exit 0; }
# Never promote an unhealthy standby.
curl -fsS --max-time 1 "http://127.0.0.1:${STANDBY}/healthz" >/dev/null || { log 'standby unhealthy; controller will handle recovery'; systemctl --no-block start gptadmin-failover-controller.service || true; exit 0; }
tmp=$(mktemp /etc/nginx/conf.d/.gptadmin-hub-upstream.XXXXXX)
cat >"$tmp" <<CFG
upstream gptadmin_hub_active {
    zone gptadmin_hub_active 64k;
    server 127.0.0.1:${STANDBY} max_fails=1 fail_timeout=1s;
    server 127.0.0.1:${PRIMARY} backup;
    keepalive 64;
}
CFG
chmod 0644 "$tmp"
mv "$tmp" "$UPSTREAM"
nginx -t >/dev/null
systemctl reload nginx
now=$(date +%s)
python3 - "$STATE" "$now" <<'PY'
import json,sys,os
p=sys.argv[1]; now=int(sys.argv[2])
try: x=json.load(open(p))
except Exception: x={}
x.update({'active':19001,'failover_at':now,'primary_stable_since':0,'reason':'primary_exec_stop','updated_at':now})
t=p+'.tmp'; open(t,'w').write(json.dumps(x,sort_keys=True)+'\n'); os.replace(t,p)
PY
log 'promoted standby synchronously after primary stop'
systemctl --no-block start gptadmin-failover-controller.service || true
