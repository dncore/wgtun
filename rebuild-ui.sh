#!/bin/bash
# wg-service — 重建前端并重启 Web UI
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== 重建前端 ==="
sudo rm -rf "$SCRIPT_DIR/client/dist"
cd "$SCRIPT_DIR/client" && npm run build
sudo launchctl kickstart -k system/com.wireguard.ui
echo "done → http://localhost:4623"
