#!/usr/bin/env bash
# Build and obfuscate rootd and hub_proxy with pyarmor

set -euo pipefail

ART_DIR="build"
rm -rf "$ART_DIR"
mkdir -p "$ART_DIR"

python -m venv "$ART_DIR/venv"
source "$ART_DIR/venv/bin/activate"

pip install --upgrade pip >/dev/null
pip install -r requirements.txt pyarmor pyinstaller >/dev/null

# Obfuscate source files
pyarmor gen -O "$ART_DIR/rootd" rootd.py >/dev/null
pyarmor gen -O "$ART_DIR/hub_proxy" hub_proxy.py >/dev/null

# Package into single-file executables
pyinstaller "$ART_DIR/rootd/rootd.py" --onefile --distpath "$ART_DIR/rootd/dist" --workpath "$ART_DIR/rootd/build" >/dev/null
pyinstaller "$ART_DIR/hub_proxy/hub_proxy.py" --onefile --distpath "$ART_DIR/hub_proxy/dist" --workpath "$ART_DIR/hub_proxy/build" >/dev/null

# Archive artifacts
 tar -czf "$ART_DIR/gptadmin.tar.gz" -C "$ART_DIR" rootd hub_proxy

# Smoke tests
ROOTD_BIN="$ART_DIR/rootd/dist/rootd"
HUB_BIN="$ART_DIR/hub_proxy/dist/hub_proxy"

# Test rootd
ROOTD_TOKEN=testtoken HUB_URL='' "$ROOTD_BIN" >/tmp/rootd_build.log 2>&1 &
ROOTD_PID=$!
sleep 2
curl -sf -H "Authorization: Bearer testtoken" http://127.0.0.1:25900/system/health | grep -q uptime_s
kill $ROOTD_PID

# Test hub_proxy
CTL_TOKEN=ctltest "$HUB_BIN" >/tmp/hub_build.log 2>&1 &
HUB_PID=$!
sleep 2
curl -sf -X POST http://127.0.0.1:9001/heartbeat -H 'Content-Type: application/json' \
    -d '{"name":"srv","base_url":"http://127.0.0.1:25900","rootd_token":"x","time":0}' >/dev/null
curl -sf http://127.0.0.1:9001/servers | grep -q srv
kill $HUB_PID

echo "Artifacts stored in $ART_DIR"
