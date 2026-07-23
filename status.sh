#!/bin/bash
# wg-service — 状态检查

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/config.sh"

echo "=== wg-service 状态 ==="
echo ""

# 1. 隧道状态
echo "--- 隧道 ---"
WG_OUTPUT=$($WG_BIN show all 2>/dev/null)
if [ -z "$WG_OUTPUT" ]; then
    for iface in "${INTERFACES[@]}"; do
        echo "  ❌ $iface 未运行 (wg show all 无输出)"
    done
else
    while IFS= read -r line; do
        if [[ "$line" =~ ^interface:\ (.+) ]]; then
            local_iface="${BASH_REMATCH[1]}"
        fi
        if [[ "$line" =~ ^[[:space:]]*endpoint:\ (.+) ]]; then
            local_endpoint="${BASH_REMATCH[1]}"
        fi
        if [[ "$line" =~ ^[[:space:]]*latest\ handshake:\ (.+) ]]; then
            local_handshake="${BASH_REMATCH[1]}"
        fi
        if [[ "$line" =~ ^[[:space:]]*transfer:\ (.+) ]]; then
            local_transfer="${BASH_REMATCH[1]}"
            printf "  ✅ %-8s  endpoint=%-35s  握手=%-12s  传输=%s\n" \
                "$local_iface" "$local_endpoint" "$local_handshake" "$local_transfer"
        fi
    done <<< "$WG_OUTPUT"
fi

# 2. daemon 状态
echo ""
echo "--- LaunchDaemon ---"
for iface in "${INTERFACES[@]}"; do
    if launchctl list 2>/dev/null | grep -q "com.wireguard.${iface}"; then
        echo "  ✅ com.wireguard.${iface} 已加载"
    else
        echo "  ❌ com.wireguard.${iface} 未加载"
    fi
done
for svc in healthcheck logrotate ui; do
    if launchctl list 2>/dev/null | grep -q "com.wireguard.$svc"; then
        echo "  ✅ com.wireguard.$svc 已加载"
    else
        echo "  ❌ com.wireguard.$svc 未加载"
    fi
done

# 3. Web UI
echo ""
if curl -s -o /dev/null -w "%{http_code}" "http://localhost:$UI_PORT" 2>/dev/null | grep -q 200; then
    echo "  ✅ Web UI 可访问: http://localhost:$UI_PORT"
else
    echo "  ❌ Web UI 不可访问: http://localhost:$UI_PORT"
fi

# 4. sleepwatcher
echo ""
echo "--- sleepwatcher ---"
if pgrep -f sleepwatcher &>/dev/null; then
    echo "  ✅ sleepwatcher 运行中"
else
    echo "  ❌ sleepwatcher 未运行"
fi
if [ -x "$WAKEUP_SCRIPT" ]; then
    echo "  ✅ $WAKEUP_SCRIPT 就绪"
else
    echo "  ❌ $WAKEUP_SCRIPT 缺失"
fi

echo ""
echo "=== 日志: $LOG_DIR ==="
