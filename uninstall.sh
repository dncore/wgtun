#!/bin/bash
# WireGuard Dashboard — 一键卸载
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/config.sh"

echo "=== WireGuard Dashboard 卸载 ==="
echo ""

echo "[1/3] 停止并移除 LaunchDaemon…"
for iface in "${INTERFACES[@]}"; do
    launchctl bootout system "$PLIST_DIR/com.wireguard.${iface}.plist" 2>/dev/null || true
    rm -f "$PLIST_DIR/com.wireguard.${iface}.plist"
    echo "  -> 已移除 com.wireguard.${iface}"
done
launchctl bootout system "$PLIST_DIR/com.wireguard.healthcheck.plist" 2>/dev/null || true
rm -f "$PLIST_DIR/com.wireguard.healthcheck.plist"
echo "  -> 已移除 com.wireguard.healthcheck"
launchctl bootout system "$PLIST_DIR/com.wireguard.logrotate.plist" 2>/dev/null || true
rm -f "$PLIST_DIR/com.wireguard.logrotate.plist"
echo "  -> 已移除 com.wireguard.logrotate"
launchctl bootout system "$PLIST_DIR/com.wireguard.ui.plist" 2>/dev/null || true
rm -f "$PLIST_DIR/com.wireguard.ui.plist"
echo "  -> 已移除 com.wireguard.ui"

echo ""
echo "[2/3] 停止 sleepwatcher…"
brew services stop sleepwatcher 2>/dev/null || true
rm -f "$WAKEUP_SCRIPT"
echo "  -> 已移除 $WAKEUP_SCRIPT"

echo ""
read -p "删除日志目录 $LOG_DIR？[y/N] " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    rm -rf "$LOG_DIR"
    echo "  -> 已移除 $LOG_DIR"
else
    echo "  -> 保留 $LOG_DIR"
fi

echo ""
echo "=== 卸载完成 ==="
