#!/usr/bin/env bash
set -euo pipefail

# This uses your real Google Chrome profile. Close Chrome first, or Chrome may ignore the debugging flag.
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
PROFILE_DIR="$HOME/Library/Application Support/Google/Chrome"

exec "$CHROME" \
  --remote-debugging-port=9222 \
  --user-data-dir="$PROFILE_DIR"
