# wg-service v2 重构方案 — wireguard-go 编排器（Go TUI）

## 定性

用 Go 重写整个项目：单二进制 `wgs`（bubbletea TUI，普通用户运行）+ root 权限
daemon（launchd 常驻，编排 wireguard-go 子进程），brew 分发。彻底替换现有
shell 脚本 + Python Web UI 体系（此前的僵尸进程 / already exists 死锁 /
日志无时间戳等问题的结构性根因一并消除）。

## 架构

```
┌─────────────┐  HTTP over Unix socket   ┌─────────────────────────────┐
│  wgs (TUI)  │◄─────────────────────────►│  wgs daemon (root)          │
│  bubbletea  │   /var/run/wgs.sock       │  launchd com.wgs.daemon     │
│  普通用户    │   (root:admin 0660)       │  ├ 实例监督器(reconcile)     │
└─────────────┘                           │  │   └ wireguard-go 子进程×N │
                                          │  ├ wgctrl UAPI 查询/下发配置 │
                                          │  ├ 期望状态存储(开机自启)     │
                                          │  └ 事件日志(ring+JSONL 落盘)  │
                                          └─────────────────────────────┘
```

- **隧道实现**：daemon 为每个配置 fork 一个 `wireguard-go` 子进程，独立运行时目录
  `/var/run/wgs/<name>/`（`WG_UAPI_DIR` + `WG_TUN_NAME_FILE` 环境变量，摆脱
  `/var/run/wireguard` 共享目录残留文件问题）；配置下发/握手/流量查询用
  wgctrl-go（macOS 走 userspace UAPI，已验证支持）；地址/MTU/路由用受控
  `ifconfig`/`route` exec。子进程是 daemon 亲子，不再需要 AbandonProcessGroup。
- **daemon 崩溃自愈**：launchd KeepAlive 重启 daemon → 按实例 pid 文件 +
  UAPI 探活**收养**存活实例，僵死的清理重拉（把这次手工 runbook 变成代码）。
- **开机自启**：单一 `com.wgs.daemon`（RunAtLoad），per-instance 的自启动只是
  daemon 期望状态里的 enabled 标志——不再生成一堆 per-interface plist。
- **配置目录**：沿用 `/usr/local/etc/wireguard/*.conf`（首次运行自动导入现有
  wg0/wg1，enabled 沿用当前运行状态）。

## 仓库布局

```
wg-service/
├── main.go                  # wgs → TUI；wgs daemon；wgs daemon --install/--uninstall；wgs version
├── go.mod                   # module github.com/dncore/wg-service
├── internal/
│   ├── wgconf/              # .conf 解析/序列化(wg-quick 兼容)、wgtypes 密钥生成、
│   │   └── *_test.go        #   ListenPort 跨配置冲突 + lsof 在用检测、CIDR/密钥校验
│   ├── daemon/              # 监督器、reconcile/收养、netsetup(ifconfig/route)、
│   │                        #   launchd plist 安装
│   ├── api/                 # HTTP-over-unix-socket 服务端 + TUI 用的客户端封装
│   ├── state/               # /var/lib/wgs/state.json（enabled 等期望状态）
│   ├── logs/                # 内存 ring(10k) + /var/log/wgs/events.jsonl 落盘 +
│   │                        #   尺寸轮转 + 查询/订阅
│   ├── i18n/                # en/zh 字符串表（默认英文，运行时切换）
│   └── tui/                 # app.go + 各 tab 模型 + 主题/按键/help
├── Formula/wgs.rb           # brew formula（含 service 块）
├── .github/workflows/       # goreleaser → dncore/homebrew-tap 发布
└── README.md                # 重写（双语）
```

删除：server.py / server.mjs / client/ / install.sh / healthcheck.sh / config.sh /
restart.sh / status.sh / ts.sh / uninstall.sh / fix 脚本。

## API（localhost-only Unix socket，JSON）

```
GET    /state                       # daemon 版本/uptime/概览
GET    /instances                   # 列表(含 enabled/running/冲突标记)
POST   /instances                   # 新建(name + conf 内容)
PUT    /instances/{name}            # 编辑 conf
DELETE /instances/{name}            # 删除(可选拒绝删除运行中)
POST   /instances/{name}/up|down|restart
PATCH  /instances/{name}            # enabled(开机自启) 开关
GET    /instances/{name}/status     # wgctrl 实时数据(握手年龄/rx/tx/peers)
GET    /logs?instance=&level=&q=&since=&until=   # 历史查询(日志回放)
GET    /logs?follow=1               # 流式订阅(chunked)
```

## TUI（bubbletea v1 + bubbles + lipgloss，5 个 tab）

1. **Dashboard**：全局概览(实例在线数/总流量/daemon uptime) + 每实例卡片：
   状态、endpoint、最新握手年龄(按新鲜度着色)、rx/tx、在线 peer 数；
   选中实例吞吐 sparkline(asciigraph)；2s 轮询。
2. **Instances**：表格(名称/自启★/运行●/ListenPort/peer 数/冲突列)；
   u/d/r 起停重启、enter 详情、n 新建、e 编辑、x 删除(确认弹窗)、c 克隆。
3. **Editor**：表单编辑 Interface(PrivateKey 可一键生成/轮换) + Peers 子列表
   (增删改：PublicKey/AllowedIPs/Endpoint/Keepalive/PSK 生成)；
   保存时内联校验：CIDR/密钥格式、ListenPort 与其他配置冲突、与系统在用端口冲突。
   编辑了运行中实例 → 提示重启生效。
4. **Logs**：按实例/级别/时间范围/关键字过滤；回放(历史查询)与 follow 实时
   追随两种模式；级别着色。
5. **Settings**：语言 English/中文(即时切换，持久化到 ~/.config/wgs/)、
   daemon 状态与路径信息、重启 daemon。

i18n：全部 UI 文案走 key → en/zh 映射；默认英文；daemon 侧系统日志固定英文。

## brew 发布

- goreleaser 构建 darwin/arm64 + amd64，tag 触发 Actions 写入 `dncore/homebrew-tap`。
- `brew install dncore/tap/wgs`；两种服务安装方式并存：
  - `sudo brew services start wgs`（formula service 块，root LaunchDaemon）
  - `sudo wgs daemon --install`（自装 plist，不依赖 brew services 行为）
- 开发期可直接 `go build && sudo ./wgs daemon`（前台）联调。

## 实施阶段（每步可验证）

1. wgconf 包 + 单测：解析现有 wg0/wg1.conf roundtrip、密钥生成、冲突检测 → `go test ./...`
2. daemon 核心：子进程编排 + wgctrl 配置 + netsetup + state + 事件日志 → 前台跑通，curl socket 可 up/down
3. API 完整端点 + 客户端封装 + 收养/自愈逻辑 → kill -9 daemon 后 launchd 拉起并收养实例
4. TUI 骨架 + i18n + Settings（语言切换持久化）
5. Instances CRUD + Editor + 冲突检测 UI
6. Dashboard + Logs（回放 + follow）
7. brew formula + goreleaser + tap workflow + README 重写 + 旧系统迁移/卸载文档
8. 终验清单：全新安装流程、重启机器自启、daemon 崩溃收养、端口冲突复现提示、
   日志轮转、中英切换、按 runbook 移除旧 shell 体系（sudo 步骤由用户执行）

## 已知取舍

- DNS 字段解析并保留，但暂不改写系统 DNS（macOS 上全局改 DNS 侵入性大；
  首版不实现 wg-quick 的 DNS 应用，README 注明）。
- sock 权限 root:admin 0660 = 本机任意 admin 可控（单用户机可接受，README 注明）。
- PostUp/PostDown 解析保留并在 up/down 时执行，保持 wg-quick 兼容。
```
