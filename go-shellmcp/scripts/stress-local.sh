#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
PORT=${PORT:-26084}
TOKEN=${TOKEN:-stress-token}
REQUESTS=${REQUESTS:-120}
WORKERS=${WORKERS:-20}
LOG_LIMIT_B=${LOG_LIMIT_B:-4096}
SPOOL=${SPOOL:-/tmp/shellmcp-go-stress-spool}
BIN=${BIN:-/tmp/shellmcp-go-stress}
rm -rf "$SPOOL"
go build -o "$BIN" ./cmd/shellmcp-go
SHELL_PORT=$PORT \
SHELL_HOST=127.0.0.1 \
SHELL_NAME=go-stress-test \
SHELL_TOKEN=$TOKEN \
SHELL_SPOOL_DIR=$SPOOL \
SHELL_HEARTBEAT=0 \
SHELL_QUEUE=0 \
SHELL_MODE=webhook \
LOG_LIMIT_B=$LOG_LIMIT_B \
"$BIN" > /tmp/shellmcp-go-stress-${PORT}.log 2>&1 &
PID=$!
cleanup(){ kill "$PID" 2>/dev/null || true; }
trap cleanup EXIT
sleep 1
echo "start process:"
ps -p "$PID" -o pid,etimes,rss,vsz,nlwp,stat,cmd
python3 - <<PY3
import concurrent.futures, json, urllib.request
url='http://127.0.0.1:${PORT}/exec'
headers={'Authorization':'Bearer ${TOKEN}','Content-Type':'application/json'}
def one(i):
    data=json.dumps({'cmd':f'printf job{i}; >&2 printf err{i}'}).encode()
    req=urllib.request.Request(url,data=data,headers=headers,method='POST')
    with urllib.request.urlopen(req, timeout=10) as r:
        j=json.loads(r.read())
    assert j['returncode']==0, j
    assert j['stdout']==f'job{i}', j
    assert j['stderr']==f'err{i}', j
    return i
with concurrent.futures.ThreadPoolExecutor(max_workers=${WORKERS}) as ex:
    got=list(ex.map(one, range(${REQUESTS})))
print('requests_ok', len(got), 'workers', ${WORKERS})
PY3
echo "after concurrent exec:"
ps -p "$PID" -o pid,etimes,rss,vsz,nlwp,stat,cmd
JOB=$(curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"cmd":"sleep 0.2; printf bg","background":true}' \
  "http://127.0.0.1:${PORT}/exec" | python3 -c 'import sys,json; print(json.load(sys.stdin)["job_id"])')
sleep 1
curl -fsS -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:${PORT}/jobs/$JOB" | python3 -m json.tool | sed -n '1,80p'
RES=$(curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"cmd":"python3 - <<PY4\nprint(\"z\"*20000)\nPY4"}' \
  "http://127.0.0.1:${PORT}/exec")
printf '%s\n' "$RES" | python3 -m json.tool | sed -n '1,80p'
P=$(printf '%s' "$RES" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("stdout_path",""))')
echo "spill_file_size=$(stat -c %s "$P")"
echo "log:"
sed -n '1,120p' /tmp/shellmcp-go-stress-${PORT}.log
