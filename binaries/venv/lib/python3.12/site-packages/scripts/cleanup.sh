#!/bin/bash
# Cleanup old gptadmin outputs and logs (older than 7 days)
set -euo pipefail

PROJECT_DIR="/home/roomhacker/gptadmin"
DAYS=7

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Starting cleanup..."

# Clean old outputs
find "$PROJECT_DIR/config/outputs" -type f -mtime +$DAYS -delete 2>/dev/null || true

# Clean old logs
find "$PROJECT_DIR/logs" -type f -mtime +$DAYS -delete 2>/dev/null || true

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Cleanup completed"
