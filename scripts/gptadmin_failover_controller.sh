#!/usr/bin/env bash
set -euo pipefail

LOCK=/run/gptadmin-failover-controller.lock
STATE=/run/gptadmin-failover-controller.json
UPSTREAM=/etc/nginx/conf.d/gptadmin-hub-upstream.conf
BIN=/opt/gptadmin/bin/gptadmin_hub
PRIMARY_SVC=gptadmin-hub.service
STANDBY_SVC=gptadmin-hub-standby.service
PRIMARY_PORT=9001
STANDBY_PORT=19001
FAILBACK_STABLE_SEC=${GPTADMIN_FAILOVER_STABLE_SEC:-30}
HEALTH_TIMEOUT=${GPTADMIN_FAILOVER_HEALTH_TIMEOUT:-2}

log(){ logger -t gptadmin-failover-controller -- "$*"; echo "$*"; }
now(){ date +%s; }
health(){ curl -fsS --max-time "$HEALTH_TIMEOUT" "http://127.0.0.1:$1/healthz" >/dev/null 2>&1; }
svc_active(){ systemctl is-active --quiet "$1"; }
proc_hash(){ local svc=$1 pid; pid=$(systemctl show "$svc" -p MainPID --value 2>/dev/null || echo 0); [ "$pid" != 0 ] && [ -r "/proc/$pid/exe" ] && sha256sum "/proc/$pid/exe" | awk '{print $1}' || true; }
installed_hash(){ sha256sum "$BIN" | awk '{print $1}'; }
active_port(){
  if grep -q "server 127.0.0.1:${PRIMARY_PORT} max_fails" "$UPSTREAM" 2>/dev/null; then echo "$PRIMARY_PORT";
  elif grep -q "server 127.0.0.1:${STANDBY_PORT} max_fails" "$UPSTREAM" 2>/dev/null; then echo "$STANDBY_PORT";
  else echo unknown; fi
}
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
  nginx -t >/dev/null
  systemctl reload nginx
}
json_state(){
  python3 - "$STATE" "$@" <<'PY'
import json,sys,time,os
p=sys.argv[1]
x={}
try: x=json.load(open(p))
except Exception: pass
for item in sys.argv[2:]:
    k,v=item.split('=',1)
    if v in ('true','false'): v=(v=='true')
    else:
        try: v=int(v)
        except Exception: pass
    x[k]=v
x['updated_at']=int(time.time())
tmp=p+'.tmp'
open(tmp,'w').write(json.dumps(x,sort_keys=True)+'\n')
os.replace(tmp,p)
PY
}
state_get(){ python3 - "$STATE" "$1" <<'PY'
import json,sys
try: x=json.load(open(sys.argv[1])); print(x.get(sys.argv[2],''))
except Exception: print('')
PY
}
wait_healthy(){ local port=$1 attempts=${2:-50}; for _ in $(seq 1 "$attempts"); do health "$port" && return 0; sleep .2; done; return 1; }
unit_busy(){
  local svc=$1 state
  state=$(systemctl show "$svc" -p ActiveState --value 2>/dev/null || true)
  [ "$state" = "activating" ] || [ "$state" = "deactivating" ]
}
restart_and_wait(){
  local svc=$1 port=$2
  if unit_busy "$svc"; then log "defer repair: $svc state is changing"; return 75; fi
  systemctl reset-failed "$svc" 2>/dev/null || true
  systemctl restart "$svc"
  wait_healthy "$port" 75
}

exec 9>"$LOCK"
flock -n 9 || { log 'skip: controller already running'; exit 0; }

# Manual graceful handover owns topology while active.
if systemctl is-active --quiet gptadmin-handover@restart-primary.service; then
  log 'skip: manual handover active'
  exit 0
fi

installed=$(installed_hash)
phealth=false; shealth=false
health "$PRIMARY_PORT" && phealth=true
health "$STANDBY_PORT" && shealth=true
active=$(active_port)
phash=$(proc_hash "$PRIMARY_SVC")
shash=$(proc_hash "$STANDBY_SVC")

json_state active="$active" primary_healthy="$phealth" standby_healthy="$shealth" installed_hash="$installed" primary_hash="$phash" standby_hash="$shash"

# Traffic failover has higher priority than process repair. If the currently
# active Hub is unhealthy, move nginx to the healthy peer immediately even if
# systemd is already restarting the failed process. This keeps hard crashes
# (SIGKILL/OOM) out of the systemd transition-delay path.
if [ "$active" = "$PRIMARY_PORT" ] && [ "$phealth" != true ] && [ "$shealth" = true ]; then
  log 'FAILOVER FAST: active primary unhealthy -> standby active'
  write_upstream "$STANDBY_PORT" "$PRIMARY_PORT"
  active=$STANDBY_PORT
  json_state active="$active" failover_at="$(now)" primary_stable_since=0 reason=primary_unhealthy_fast
fi
if [ "$active" = "$STANDBY_PORT" ] && [ "$shealth" != true ] && [ "$phealth" = true ]; then
  log 'FAILBACK FAST: active standby unhealthy -> primary active'
  write_upstream "$PRIMARY_PORT" "$STANDBY_PORT"
  active=$PRIMARY_PORT
  json_state active="$active" primary_stable_since=0 reason=standby_unhealthy_fast
fi

if [ "$shealth" != true ] && unit_busy "$STANDBY_SVC"; then
  log 'defer: standby systemd transition in progress'
  json_state reason=standby_transition
  exit 0
fi
if [ "$phealth" != true ] && unit_busy "$PRIMARY_SVC"; then
  log 'defer: primary systemd transition in progress'
  json_state reason=primary_transition
  exit 0
fi

# Repair missing services first. Prefer repairing the inactive peer so traffic stays up.
if [ "$active" = "$PRIMARY_PORT" ]; then
  if [ "$shealth" != true ] || [ "$shash" != "$installed" ]; then
    log "repair standby: healthy=$shealth running_hash=${shash:-missing} installed=$installed"
    restart_and_wait "$STANDBY_SVC" "$STANDBY_PORT" || { log 'standby repair deferred/failed'; json_state last_error=standby_repair_failed; exit 0; }
    shealth=true; shash=$(proc_hash "$STANDBY_SVC")
  fi
elif [ "$active" = "$STANDBY_PORT" ]; then
  if [ "$phealth" != true ]; then
    log 'repair primary while standby active'
    restart_and_wait "$PRIMARY_SVC" "$PRIMARY_PORT" || { log 'primary repair deferred/failed'; json_state last_error=primary_repair_failed; exit 0; }
    phealth=true; phash=$(proc_hash "$PRIMARY_SVC")
  fi
fi

# If active primary died, immediately promote healthy standby.
if [ "$active" = "$PRIMARY_PORT" ] && [ "$phealth" != true ]; then
  if [ "$shealth" != true ]; then
    log 'both hubs unhealthy: attempt standby recovery first'
    restart_and_wait "$STANDBY_SVC" "$STANDBY_PORT" || true
    health "$STANDBY_PORT" && shealth=true
  fi
  if [ "$shealth" = true ]; then
    log 'FAILOVER: primary unhealthy -> standby active'
    write_upstream "$STANDBY_PORT" "$PRIMARY_PORT"
    active=$STANDBY_PORT
    json_state active="$active" failover_at="$(now)" primary_stable_since=0 reason=primary_unhealthy
    # Repair primary only after traffic is safely on standby.
    restart_and_wait "$PRIMARY_SVC" "$PRIMARY_PORT" || true
    exit 0
  fi
  log 'CRITICAL: both primary and standby unhealthy'
  json_state last_error=both_hubs_unhealthy
  exit 0
fi

# Installed binary changed while primary still runs old code: self-roll safely through standby.
if [ "$active" = "$PRIMARY_PORT" ] && [ "$phealth" = true ] && [ "$phash" != "$installed" ]; then
  if [ "$shealth" != true ] || [ "$shash" != "$installed" ]; then
    log 'prepare standby with new installed binary before rolling primary'
    restart_and_wait "$STANDBY_SVC" "$STANDBY_PORT" || { json_state last_error=standby_update_prepare_failed; exit 0; }
  fi
  log "ROLLING UPDATE: promote standby before primary restart old=$phash new=$installed"
  write_upstream "$STANDBY_PORT" "$PRIMARY_PORT"
  active=$STANDBY_PORT
  json_state active="$active" failover_at="$(now)" primary_stable_since=0 reason=primary_binary_drift
  restart_and_wait "$PRIMARY_SVC" "$PRIMARY_PORT" || { log 'primary failed after rolling update'; json_state last_error=primary_rolling_update_failed; exit 0; }
  exit 0
fi

# If standby is active, keep it serving while primary proves stable. Then fail back automatically.
if [ "$active" = "$STANDBY_PORT" ]; then
  # If standby itself died and primary is healthy, fail back immediately.
  if [ "$shealth" != true ] && [ "$phealth" = true ]; then
    log 'FAILBACK EMERGENCY: standby unhealthy, primary healthy'
    write_upstream "$PRIMARY_PORT" "$STANDBY_PORT"
    json_state active="$PRIMARY_PORT" primary_stable_since=0 reason=standby_unhealthy
    exit 0
  fi
  if [ "$phealth" = true ]; then
    # Ensure primary actually runs installed binary before considering failback.
    phash=$(proc_hash "$PRIMARY_SVC")
    if [ "$phash" != "$installed" ]; then
      log "primary healthy but hash stale ($phash != $installed), restarting while standby active"
      restart_and_wait "$PRIMARY_SVC" "$PRIMARY_PORT" || { json_state last_error=primary_hash_repair_failed; exit 0; }
      json_state primary_stable_since="$(now)"
      exit 0
    fi
    stable_since=$(state_get primary_stable_since)
    case "$stable_since" in ''|0) stable_since=$(now); json_state primary_stable_since="$stable_since";; esac
    age=$(( $(now) - stable_since ))
    if [ "$age" -ge "$FAILBACK_STABLE_SEC" ]; then
      log "FAILBACK: primary stable ${age}s -> primary active"
      write_upstream "$PRIMARY_PORT" "$STANDBY_PORT"
      json_state active="$PRIMARY_PORT" primary_stable_since=0 reason=primary_recovered
      # Do not restart standby here; existing keep-alive/long-poll requests may still drain.
      exit 0
    fi
    log "standby active; primary healthy for ${age}s/${FAILBACK_STABLE_SEC}s"
    exit 0
  fi
  log 'standby active; primary still unhealthy'
  exit 0
fi

# Unknown/broken nginx topology: choose a healthy deterministic active node.
if [ "$active" = unknown ]; then
  if [ "$phealth" = true ]; then
    log 'repair nginx topology -> primary active'
    write_upstream "$PRIMARY_PORT" "$STANDBY_PORT"
    json_state active="$PRIMARY_PORT" reason=nginx_topology_repair
  elif [ "$shealth" = true ]; then
    log 'repair nginx topology -> standby active'
    write_upstream "$STANDBY_PORT" "$PRIMARY_PORT"
    json_state active="$STANDBY_PORT" reason=nginx_topology_repair
  else
    log 'CRITICAL: nginx topology unknown and both hubs unhealthy'
    exit 0
  fi
  exit 0
fi

log "ok: active=$active primary=$phealth standby=$shealth hash=$installed"
json_state last_error='' reason=healthy
