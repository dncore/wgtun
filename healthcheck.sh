#!/bin/bash
# wg-service — 健康检查
# 每 N 秒由 launchd 触发，接口正常时静默退出。
# 日志超过 LOG_MAX_SIZE 自动截断。

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/config.sh"

LOG_OUT="$LOG_DIR/healthcheck.out.log"
LOG_ERR="$LOG_DIR/healthcheck.err.log"

truncate_if_needed() {
    local f=$1
    if [ -f "$f" ]; then
        local sz
        sz=$(stat -f%z "$f" 2>/dev/null || echo 0)
        if [ "$sz" -gt "$LOG_MAX_SIZE" ]; then
            echo "[$(date '+%Y-%m-%d %H:%M:%S')] log auto-truncated (was $(du -h "$f" 2>/dev/null | cut -f1))" > "$f"
        fi
    fi
}

truncate_if_needed "$LOG_OUT"
truncate_if_needed "$LOG_ERR"

# 统计实际活跃的 WireGuard 接口数（macOS 上返回 utun 名，Linux 上返回 wg 名）
active_count=$($WG_BIN show interfaces 2>/dev/null | wc -w | tr -d ' ')
expected=${#INTERFACES[@]}

if [ "${active_count:-0}" -ge "$expected" ]; then
    exit 0
fi

# 有接口缺失 → 记录并尝试拉起
echo "$(date): only ${active_count:-0}/${expected} interfaces up, restarting missing…"

for conf in "${INTERFACES[@]}"; do
    if $WG_QUICK up "$conf" 2>&1 | "$SCRIPT_DIR/ts.sh"; then
        echo "$(date): $conf restarted successfully"
    else
        echo "$(date): ERROR: $conf restart failed" >&2
    fi
done
