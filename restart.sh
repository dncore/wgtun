#!/bin/bash
# WireGuard Dashboard — 重启隧道
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/config.sh"

echo "=== 重启 WireGuard 隧道 ==="
echo ""

# 先获取当前活跃接口列表，用于 down 操作
active_ifaces=$($WG_BIN show interfaces 2>/dev/null || true)

for conf in "${INTERFACES[@]}"; do
    echo "--- $conf ---"
    # down: 用 config 名调用 wg-quick down（它会处理名称映射）
    $BASH_BIN $WG_QUICK down "$conf" 2>/dev/null || true
    echo "  已关闭"
    # up
    $BASH_BIN $WG_QUICK up "$conf"
    echo "  已启动"
    echo ""
done

echo "=== 完成 ==="
echo ""
$WG_BIN show all 2>/dev/null || echo "(需要 sudo 查看详情: sudo wg show all)"
