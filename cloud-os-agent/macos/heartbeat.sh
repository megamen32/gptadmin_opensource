#!/bin/sh
set -eu

hub='http://192.168.2.100:9001/api/v1/cloud-os'
state_dir="$HOME/.local/share/gptadmin-cloudos"
state_file="$state_dir/computer-id"
mkdir -p "$state_dir"

pair() {
  response=$(curl -fsS --max-time 10 -X POST "$hub/computers/pair" \
    -H 'Content-Type: application/json' \
    -d '{"name":"Mac mini","os":"macos","capabilities":["status"],"session_id":"mac-mini"}')
  computer_id=$(printf '%s' "$response" | sed -nE 's/.*"id":"([^"]+)".*/\1/p')
  test -n "$computer_id"
  printf '%s' "$computer_id" > "$state_file"
}

if test -f "$state_file"; then
  computer_id=$(cat "$state_file")
  if curl -fsS --max-time 10 -X POST "$hub/computers/$computer_id/heartbeat" >/dev/null 2>&1; then
    exit 0
  fi
fi

pair
