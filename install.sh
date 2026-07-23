#!/bin/bash
# WireGuard Dashboard — 一键安装
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/config.sh"

echo "=== WireGuard Dashboard 安装 ==="
echo ""

# ---- 检查 .env ----
if [ ! -f "$SCRIPT_DIR/.env" ]; then
    echo "⚠ 未找到 .env 文件，使用默认配置"
    echo "  建议: cp .env.example .env 并修改密码"
    echo ""
fi

# ---- 生成 plist 文件 ----

echo "[1/6] 生成 LaunchDaemon plist…"
mkdir -p "$LOG_DIR"

# 接口启动 plist
for iface in "${INTERFACES[@]}"; do
    cat > "$SCRIPT_DIR/com.wireguard.${iface}.plist" << PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.wireguard.${iface}</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BASH_BIN</string>
        <string>-c</string>
        <string>sleep 5 &amp;&amp; $BASH_BIN $WG_QUICK up $iface</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>$LOG_DIR/${iface}.err.log</string>
    <key>StandardOutPath</key>
    <string>$LOG_DIR/${iface}.out.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>$HOMEBREW_PREFIX/bin:$HOMEBREW_PREFIX/opt/wireguard-go/bin:$HOMEBREW_PREFIX/opt/wireguard-tools/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
</dict>
</plist>
PLIST_EOF
    echo "  -> 生成 com.wireguard.${iface}.plist"
done

# 健康检查 plist — 调用独立脚本
cat > "$SCRIPT_DIR/com.wireguard.healthcheck.plist" << PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.wireguard.healthcheck</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BASH_BIN</string>
        <string>$SCRIPT_DIR/healthcheck.sh</string>
    </array>
    <key>StartInterval</key>
    <integer>$HEALTHCHECK_INTERVAL</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>$LOG_DIR/healthcheck.err.log</string>
    <key>StandardOutPath</key>
    <string>$LOG_DIR/healthcheck.out.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>$HOMEBREW_PREFIX/bin:$HOMEBREW_PREFIX/opt/wireguard-go/bin:$HOMEBREW_PREFIX/opt/wireguard-tools/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
</dict>
</plist>
PLIST_EOF
echo "  -> 生成 com.wireguard.healthcheck.plist"

# 日志轮转 plist
cat > "$SCRIPT_DIR/com.wireguard.logrotate.plist" << PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.wireguard.logrotate</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BASH_BIN</string>
        <string>-c</string>
        <string>max=\$((10 * 1024 * 1024))
for f in $LOG_DIR/*.log; do
    size=\$(stat -f%z "\$f" 2&gt;/dev/null || echo 0)
    if [[ \$size -gt \$max ]]; then
        tail -n 500 "\$f" &gt; "\$f.tmp" &amp;&amp; mv "\$f.tmp" "\$f"
        echo "\$(date): rotated \$f (\$((size/1024))KB → trimmed to 500 lines)"
    fi
done</string>
    </array>
    <key>StartCalendarInterval</key>
    <dict>
        <key>Weekday</key>
        <integer>0</integer>
        <key>Hour</key>
        <integer>3</integer>
        <key>Minute</key>
        <integer>0</integer>
    </dict>
    <key>StandardErrorPath</key>
    <string>$LOG_DIR/logrotate.err.log</string>
    <key>StandardOutPath</key>
    <string>$LOG_DIR/logrotate.out.log</string>
</dict>
</plist>
PLIST_EOF
echo "  -> 生成 com.wireguard.logrotate.plist"

# Web UI plist
PYTHON_BIN="${PYTHON_BIN:-$(which python3 2>/dev/null || echo /usr/bin/python3)}"
cat > "$SCRIPT_DIR/com.wireguard.ui.plist" << PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.wireguard.ui</string>
    <key>ProgramArguments</key>
    <array>
        <string>$PYTHON_BIN</string>
        <string>$SCRIPT_DIR/server.py</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>$LOG_DIR/ui.err.log</string>
    <key>StandardOutPath</key>
    <string>$LOG_DIR/ui.out.log</string>
    <key>WorkingDirectory</key>
    <string>$SCRIPT_DIR</string>
</dict>
</plist>
PLIST_EOF
echo "  -> 生成 com.wireguard.ui.plist"

# ---- 构建前端 ----
echo ""
echo "[2/6] 构建 Web 前端…"
CLIENT_DIR="$SCRIPT_DIR/client"
if [ -f "$CLIENT_DIR/package.json" ]; then
    cd "$CLIENT_DIR"
    npm install --silent 2>/dev/null
    npm run build 2>/dev/null
    echo "  -> 前端已构建: $CLIENT_DIR/dist"
    cd "$SCRIPT_DIR"
else
    echo "  -> (跳过，未找到 client/package.json)"
fi

# ---- 部署 plist ----
echo ""
echo "[3/6] 部署 LaunchDaemon 到 $PLIST_DIR…"
for iface in "${INTERFACES[@]}"; do
    cp "$SCRIPT_DIR/com.wireguard.${iface}.plist" "$PLIST_DIR/"
    echo "  -> $PLIST_DIR/com.wireguard.${iface}.plist"
done
cp "$SCRIPT_DIR/com.wireguard.healthcheck.plist" "$PLIST_DIR/"
echo "  -> $PLIST_DIR/com.wireguard.healthcheck.plist"
cp "$SCRIPT_DIR/com.wireguard.logrotate.plist" "$PLIST_DIR/"
echo "  -> $PLIST_DIR/com.wireguard.logrotate.plist"
cp "$SCRIPT_DIR/com.wireguard.ui.plist" "$PLIST_DIR/"
echo "  -> $PLIST_DIR/com.wireguard.ui.plist"

# ---- 加载 daemon ----
echo ""
echo "[4/6] 加载 LaunchDaemon…"
for iface in "${INTERFACES[@]}"; do
    launchctl bootout system "$PLIST_DIR/com.wireguard.${iface}.plist" 2>/dev/null || true
    launchctl bootstrap system "$PLIST_DIR/com.wireguard.${iface}.plist"
    echo "  -> com.wireguard.${iface}"
done
launchctl bootout system "$PLIST_DIR/com.wireguard.healthcheck.plist" 2>/dev/null || true
launchctl bootstrap system "$PLIST_DIR/com.wireguard.healthcheck.plist"
echo "  -> com.wireguard.healthcheck"
launchctl bootout system "$PLIST_DIR/com.wireguard.logrotate.plist" 2>/dev/null || true
launchctl bootstrap system "$PLIST_DIR/com.wireguard.logrotate.plist"
echo "  -> com.wireguard.logrotate"
launchctl bootout system "$PLIST_DIR/com.wireguard.ui.plist" 2>/dev/null || true
launchctl bootstrap system "$PLIST_DIR/com.wireguard.ui.plist"
echo "  -> com.wireguard.ui (Web UI: http://localhost:$UI_PORT)"

# ---- sleepwatcher ----
echo ""
echo "[5/6] 安装 sleepwatcher 唤醒脚本…"
cat > "$WAKEUP_SCRIPT" << WAKEUP_EOF
#!/bin/bash
sleep 3
if ! $WG_BIN show interfaces | grep -q .; then
    for iface in ${INTERFACES[*]}; do
        $BASH_BIN $WG_QUICK up "\$iface" 2>&1 | logger -t "wireguard-wakeup"
    done
fi
WAKEUP_EOF
chmod +x "$WAKEUP_SCRIPT"
echo "  -> $WAKEUP_SCRIPT"

if ! brew list sleepwatcher &>/dev/null; then
    brew install sleepwatcher
fi
brew services start sleepwatcher 2>/dev/null || true
echo "  -> sleepwatcher started"

# ---- 验证 ----
echo ""
echo "[6/6] 验证 — 等待 10s…"
sleep 10
echo ""
launchctl list 2>/dev/null | grep wireguard || true
echo ""
$WG_BIN show all 2>/dev/null || echo "(需要 sudo 查看详情: sudo wg show all)"
echo ""
echo "=== 安装完成 ==="
echo "Web UI: http://localhost:$UI_PORT"
