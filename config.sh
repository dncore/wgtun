#!/bin/bash
# wg-service — 共享配置
# 优先级：环境变量 > .env 文件 > 默认值

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ---- 从 .env 加载 ----
if [ -f "$SCRIPT_DIR/.env" ]; then
    set -a
    source "$SCRIPT_DIR/.env"
    set +a
fi

# ---- 路径（可覆盖）----
HOMEBREW_PREFIX="${HOMEBREW_PREFIX:-/opt/homebrew}"
BASH_BIN="${BASH_BIN:-$HOMEBREW_PREFIX/bin/bash}"
WG_BIN="${WG_BIN:-$HOMEBREW_PREFIX/opt/wireguard-tools/bin/wg}"
WG_QUICK="${WG_QUICK:-$HOMEBREW_PREFIX/opt/wireguard-tools/bin/wg-quick}"
WG_GO_BIN="${WG_GO_BIN:-$HOMEBREW_PREFIX/bin/wireguard-go}"

# ---- 接口列表 ----
INTERFACES=(${INTERFACES:-wg0})

# ---- 部署路径 ----
PLIST_DIR="${PLIST_DIR:-/Library/LaunchDaemons}"
CONFIG_DIR="${CONFIG_DIR:-/usr/local/etc/wireguard}"
LOG_DIR="${LOG_DIR:-/var/log/wireguard}"
UI_PORT="${UI_PORT:-4623}"

# ---- 健康检查 ----
HEALTHCHECK_INTERVAL="${HEALTHCHECK_INTERVAL:-60}"
LOG_MAX_SIZE="${LOG_MAX_SIZE:-10485760}"

# ---- sleepwatcher ----
WAKEUP_SCRIPT="${WAKEUP_SCRIPT:-$HOME/.wakeup}"
