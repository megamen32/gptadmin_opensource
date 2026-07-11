#!/bin/bash
set -e
cd /home/roomhacker/gptadmin/website
echo "=== git pull ==="
git pull --no-edit
echo "=== build ==="
bun run build
echo "=== restart service ==="
sudo systemctl restart gptadminwebsite-next.service
echo "=== status ==="
systemctl status gptadminwebsite-next.service --no-pager -l | grep Active
echo "=== done ==="
