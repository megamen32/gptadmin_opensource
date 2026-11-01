#!/usr/bin/env python3
from __future__ import annotations
import json, os, subprocess, time
from pathlib import Path

STATE=Path('/var/lib/gptadmin/wanb-health.json')
ROUTER='root@192.168.2.1'
SSH=['ssh','-o','BatchMode=yes','-o','ConnectTimeout=3',ROUTER]
FAIL_THRESHOLD=3
BOUNCE_COOLDOWN=1800

def run(cmd, timeout=8):
    return subprocess.run(cmd,text=True,capture_output=True,timeout=timeout,check=False)

def ssh(cmd, timeout=8):
    return run(SSH+[cmd],timeout)

def load():
    try:return json.loads(STATE.read_text())
    except Exception:return {}

def save(x):
    STATE.parent.mkdir(parents=True,exist_ok=True)
    t=STATE.with_suffix('.tmp'); t.write_text(json.dumps(x,sort_keys=True)+'\n'); t.replace(STATE)

def main():
    now=int(time.time()); st=load()
    status=ssh("ifstatus wanb",5)
    up=False; ip=''; gw=''
    if status.returncode==0:
        try:
            x=json.loads(status.stdout); up=bool(x.get('up'))
            ips=x.get('ipv4-address') or []; ip=(ips[0].get('address') if ips else '') or ''
            for r in x.get('route') or []:
                if r.get('target')=='0.0.0.0' and int(r.get('mask',-1))==0: gw=r.get('nexthop') or ''; break
        except Exception: pass
    gateway_ok=False; dns_ok=False
    if up and gw:
        gateway_ok=ssh(f"ping -c 1 -W 1 {gw} >/dev/null 2>&1",4).returncode==0
    if up:
        # Provider DNS must answer; this catches the current half-open Beeline session.
        dns=ssh("nslookup ya.ru 85.21.192.3 2>/dev/null | grep -q '^Address.*:'",6)
        dns_ok=dns.returncode==0
    healthy=up and gateway_ok and dns_ok
    failures=0 if healthy else int(st.get('consecutive_failures',0))+1
    bounced=False
    last_bounce=int(st.get('last_bounce',0) or 0)
    if not healthy and failures>=FAIL_THRESHOLD and now-last_bounce>=BOUNCE_COOLDOWN:
        b=ssh("ifdown wanb; sleep 2; ifup wanb",12)
        bounced=(b.returncode==0); last_bounce=now if bounced else last_bounce
    out={'updated_at':now,'healthy':healthy,'wanb_up':up,'wanb_ip':ip,'gateway':gw,'gateway_ok':gateway_ok,'provider_dns_ok':dns_ok,'consecutive_failures':failures,'last_bounce':last_bounce,'bounced_this_run':bounced,'ddns_enabled':False,'reason':'healthy' if healthy else 'wanb_provider_path_degraded'}
    save(out); print(json.dumps(out,sort_keys=True))
    return 0  # degraded WANB is state, not a systemd failure storm
if __name__=='__main__': raise SystemExit(main())
