# WireGuard Dashboard

轻量级 WireGuard 管理仪表盘，专为 macOS 设计。提供 Web UI、健康检查、自动重启、休眠恢复等三层可靠性保障。

## 特性

- **Web 仪表盘** — 隧道状态、配置查看、日志浏览，密码保护
- **健康检查** — 定时检测接口存活，异常时自动拉起
- **自动启动** — 基于 launchd，开机自启、进程崩溃自动恢复
- **休眠恢复** — 通过 sleepwatcher 在 Mac 唤醒时重建隧道
- **日志管理** — 自动轮转，大文件截断保护

## 前置依赖

```bash
# macOS (Homebrew)
brew install wireguard-go wireguard-tools sleepwatcher

# Node.js (仅构建前端时需要)
brew install node

# Python 3 (运行后端，macOS 自带)
```

WireGuard 配置文件放在 `/usr/local/etc/wireguard/`（可在 `.env` 中修改）：

```
/usr/local/etc/wireguard/
├── wg0.conf
└── wg1.conf
```

## 快速开始

```bash
# 1. 克隆
git clone https://github.com/xxx/wg-dashboard.git
cd wg-dashboard

# 2. 配置
cp .env.example .env
# 编辑 .env：修改 UI_PASSWORD 和 INTERFACES

# 3. 安装
sudo bash install.sh
```

安装后访问 `http://localhost:4623`，使用 `.env` 中设置的密码登录。

## 配置

所有配置通过 `.env` 文件管理：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `UI_PASSWORD` | `admin` | Web UI 登录密码 |
| `UI_PORT` | `4623` | Web UI 端口 |
| `INTERFACES` | `wg0` | 接口列表，空格分隔 |
| `CONFIG_DIR` | `/usr/local/etc/wireguard` | WireGuard 配置目录 |
| `LOG_DIR` | `/var/log/wireguard` | 日志目录 |
| `HEALTHCHECK_INTERVAL` | `60` | 健康检查间隔（秒） |
| `LOG_MAX_SIZE` | `10485760` | 日志文件最大字节数 |
| `HOMEBREW_PREFIX` | `/opt/homebrew` | Homebrew 安装路径（Intel Mac 改为 `/usr/local`） |

## 命令参考

```bash
bash status.sh          # 查看状态
sudo bash restart.sh    # 重启所有隧道
sudo bash install.sh    # 安装/更新
sudo bash uninstall.sh  # 卸载
bash rebuild-ui.sh      # 重建前端（修改前端代码后）
```

## 架构

```
┌──────────────────────────────────────────────────┐
│  第一层：KeepAlive（进程级）                       │
│  wg-quick → wireguard-go + route monitor          │
│  任一子进程退出 → launchd 立即重启整个 job          │
│  解决：进程崩溃、偶发退出                           │
├──────────────────────────────────────────────────┤
│  第二层：健康检查（定时轮询）                       │
│  每 N 秒检查活跃接口数                              │
│  发现缺失 → 拉起 wg-quick                          │
│  解决：KeepAlive 漏掉的边界情况                     │
├──────────────────────────────────────────────────┤
│  第三层：sleepwatcher（唤醒触发）                   │
│  macOS 休眠唤醒 → ~/.wakeup 执行                   │
│  强制 wg-quick up 重建所有隧道                      │
│  解决：休眠后 UDP socket 绑定失效                   │
└──────────────────────────────────────────────────┘
```

## 日志

```
/var/log/wireguard/
├── <iface>.err.log         # 接口 stderr
├── <iface>.out.log         # 接口 stdout
├── healthcheck.err.log     # 健康检查 stderr
├── healthcheck.out.log     # 健康检查 stdout
└── logrotate.err.log       # 日志轮转 stderr
```

## 后端选择

默认使用 Python (`server.py`)，零额外依赖。也提供 Node.js 实现 (`server.mjs`)：

```bash
# 使用 Node.js 后端（需要先在 web/ 目录 npm install）
cd web && npm install && npm start
```

## 故障排查

```bash
# 查看 daemon 日志
sudo cat /var/log/wireguard/wg0.err.log

# 手动启动单个隧道
sudo wg-quick up wg0

# 验证 WireGuard 握手
sudo wg show all

# 检查 launchd 服务
sudo launchctl list | grep wireguard

# 重启 Web UI
sudo launchctl kickstart -k system/com.wireguard.ui
```

## 平台兼容性

| 功能 | macOS | Linux |
|------|-------|-------|
| Python 后端 | ✅ | ✅ |
| Node.js 后端 | ✅ | ✅ |
| launchd 集成 | ✅ | ❌ (改用 systemd) |
| sleepwatcher | ✅ | ❌ (改用 systemd-sleep) |
| 健康检查 | ✅ | ✅ |
| Web UI | ✅ | ✅ |

> Linux 支持需自行编写 systemd unit 文件替代 launchd plist，欢迎贡献。

## License

MIT
