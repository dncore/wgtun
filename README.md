# wg-service

macOS 上的 WireGuard 服务守护层。弥补官方 GUI 客户端的能力缺口：开机自启、健康检测自动重连、连接日志、多隧道同时运行。附带一个轻量 Web 仪表盘。

## 为什么不用官方客户端

macOS 版 WireGuard 官方 GUI 有四个硬伤：

| 官方 GUI | wg-service |
|----------|------------|
| 需手动启动，重启 Mac 后隧道断开 | launchd 开机自启，进程崩溃自动恢复 |
| 无健康检测，隧道静默断开后不自知 | 每分钟检查接口数，异常时自动拉起 |
| 无日志，排查连接问题只能靠猜 | stdout/stderr 完整采集，Web UI 可浏览 |
| 单隧道，切网络需手动换配置 | 多隧道并行，INTERFACES 一键管理 |

## 架构

```
┌──────────────────────────────────────────────────┐
│  launchd (macOS 系统级守护)                        │
│                                                    │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐ │
│  │ wg-quick │  │ wg-quick │  │ healthcheck.sh   │ │
│  │ wg0      │  │ wg1 ...  │  │ (每 N 秒)         │ │
│  │ KeepAlive│  │ KeepAlive│  │ 计数 → 补拉起      │ │
│  └──────────┘  └──────────┘  └──────────────────┘ │
│                                                    │
│  ┌──────────────────────────────────────────────┐ │
│  │ server.py :4623                              │ │
│  │ Web UI + API (密码鉴权)                       │ │
│  └──────────────────────────────────────────────┘ │
│                                                    │
│  sleepwatcher → ~/.wakeup (休眠唤醒后强制重建)     │
└──────────────────────────────────────────────────┘
```

三层防护：

1. **KeepAlive** — wg-quick 及其子进程任一退出，launchd 立即重启整个 job。防进程崩溃、偶发退出。
2. **Healthcheck** — 独立脚本每分钟统计 `wg show interfaces` 返回的活跃接口数，少于预期数则调用 `wg-quick up` 补拉。防 KeepAlive 漏掉的边界 case。
3. **Sleepwatcher** — macOS 休眠唤醒后执行 `~/.wakeup`，强制重建所有隧道。防休眠后 UDP socket 绑定失效。

## 依赖

```bash
brew install wireguard-go wireguard-tools sleepwatcher
```

Python 3（macOS 自带）。Node.js 仅构建前端时需用。

WireGuard 配置文件（由你自行准备）：

```
/usr/local/etc/wireguard/
├── wg0.conf
├── wg1.conf
└── ...
```

## 安装

```bash
git clone https://github.com/dncore/wg-service.git
cd wg-service
cp .env.example .env
# 编辑 .env — 修改 UI_PASSWORD 和 INTERFACES
sudo bash install.sh
```

安装后访问 `http://localhost:4623`。

## 配置

`.env`：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `UI_PASSWORD` | `admin` | Web UI 登录密码 |
| `UI_PORT` | `4623` | Web UI 端口 |
| `INTERFACES` | `wg0` | 接口列表（空格分隔） |
| `CONFIG_DIR` | `/usr/local/etc/wireguard` | 配置目录 |
| `LOG_DIR` | `/var/log/wireguard` | 日志目录 |
| `HEALTHCHECK_INTERVAL` | `60` | 健康检查间隔（秒） |
| `LOG_MAX_SIZE` | `10485760` | 日志自动截断阈值（字节） |
| `HOMEBREW_PREFIX` | `/opt/homebrew` | M 系列默认，Intel 改为 `/usr/local` |

## 命令

```bash
bash status.sh          # 查看服务状态
sudo bash restart.sh    # 重启所有隧道
sudo bash install.sh    # 安装 / 更新
sudo bash uninstall.sh  # 卸载
bash rebuild-ui.sh      # 修改前端源码后重新构建
```

## 日志

```
/var/log/wireguard/
├── wg0.err.log          # wg0 标准错误
├── wg0.out.log          # wg0 标准输出
├── wg1.err.log          # wg1 标准错误
├── wg1.out.log          # wg1 标准输出
├── healthcheck.out.log  # 健康检查输出
├── healthcheck.err.log  # 健康检查错误
├── ui.out.log           # Web 服务输出
└── ui.err.log           # Web 服务错误
```

超过 `LOG_MAX_SIZE` 的日志文件会在健康检查时自动截断。另有每周日 3:00 的 logrotate 轮转任务。

## Web UI

密码鉴权登录，提供三个面板：

- **隧道卡片** — 接口名、endpoint、握手时间、收发流量
- **配置查看** — 展示 conf 文件内容（PrivateKey 自动隐藏）
- **日志浏览** — 按服务切换，关键字高亮，支持清空

前端为 React + Vite + Tailwind，构建产物在 `client/dist/`。后端提供 Python（`server.py`，零额外依赖）和 Node.js（`server.mjs`）两种实现。

## 平台

| 功能 | macOS | Linux |
|------|-------|-------|
| launchd 守护 | ✅ | — |
| sleepwatcher 唤醒 | ✅ | — |
| 健康检查 | ✅ | ✅ |
| Web UI | ✅ | ✅ |
| Python 后端 | ✅ | ✅ |
| Node.js 后端 | ✅ | ✅ |

Linux 支持需自行编写 systemd unit 文件替代 launchd plist。

## 故障排查

```bash
sudo cat /var/log/wireguard/wg0.err.log   # 查看接口日志
sudo wg-quick up wg0                       # 手动启动
sudo wg show all                           # 握手状态
sudo launchctl list | grep wireguard        # 守护进程状态
sudo launchctl kickstart -k system/com.wireguard.ui  # 重启 Web 服务
```

## License

MIT
