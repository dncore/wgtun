# wgtun

[English](README.md) | **简体中文**

[![Release](https://img.shields.io/github/v/release/dncore/wgtun)](https://github.com/dncore/wgtun/releases)
[![License](https://img.shields.io/github/license/dncore/wgtun)](LICENSE)

macOS 上的 WireGuard 编排器 —— 一个终端 UI 加一个 root launchd 常驻进程，
替你管理任意数量的 `wireguard-go` 实例。不再有 `wg-quick` 脚本、不再有
每接口一份的 launchd plist、不需要内核扩展。

```
wgtun                 打开终端 UI
wgtun daemon          运行特权守护进程（由 launchd 拉起）
```

## 为什么做这个

macOS 没有内核态 WireGuard，所有人都在跑用户态的 `wireguard-go`。用原
生工具管理多条隧道意味着手写 shell 脚本、每个接口一份 launchd 任务，以
及一堆会以各种方式腐烂的共享运行时文件（残留锁、僵尸后端、重启时
`already exists` 死锁）。

wgtun 用两个部件取代这一切：

- **守护进程**：独占所有 wireguard-go 进程，持续将实际状态向期望状态收敛
  （崩溃后收养、退避重启、跟踪端点 DNS —— 开机未就绪与 DDNS 变更都覆盖）。
- **终端 UI**：经本地 socket 与守护进程通信，提供仪表盘、实例管理、
  配置编辑器和日志回放。

| | wg-quick + 脚本 | wgtun |
|---|---|---|
| 多隧道 | 每条一个脚本 | 任意数量，单一监督器 |
| 开机自启 | 每个接口一份 plist | 每个实例一个开关 |
| 崩溃恢复 | 通常没有 | 收养 + 自愈 + 退避 |
| 实时状态 | 解析 `wg show` 文本 | 原生 UAPI，结构化数据 |
| 编辑配置 | 手工编辑文件 | TUI 编辑器 + 冲突检测 |
| 日志 | 无时间戳的 stderr 转储 | 结构化事件，回放 + 实时跟随 |

## TUI 预览

**仪表盘（Dashboard）** —— 运行/在线/流量统计块，每个实例一张卡片，展示
完整的 peer 信息（端点、允许 IP、keepalive、PSK 标志、握手时间、字节计
数）和实时接收速率柱状图：

```
 Dashboard    Instances    Logs    Settings
 WireGuard Dashboard

 ╭───────────╮ ╭───────────────╮ ╭────────────────────╮
 │  Running  │ │  Online peers │ │  Total traffic     │
 │  2/2      │ │  2            │ │  1.4M ↓ / 3.8M ↑   │
 ╰───────────╯ ╰───────────────╯ ╰────────────────────╯

 ╭──────────────────────────────────────╮ ╭──────────────────────────────────────╮
 │ work  running  utun0                 │ │ home  running  utun7                 │
 │ listen 51820  pub AbCd…WxYz=         │ │ listen 51821  pub EfGh…YzAb=         │
 │ ● peer 1  1a2B…c3D4=  ka25           │ │ ● peer 1  5e6F…g7H8=  PSK ka25       │
 │   endpoint 203.0.113.7:51820         │ │   endpoint vpn.example.com:51820     │
 │   allowed  10.0.0.0/24, 10.0.1.0/24  │ │   allowed  10.10.0.0/24              │
 │   just now  rx 12.3M tx 4.5M         │ │   2m ago  rx 812K tx 240K            │
 │           ▁▂▃▅▇█▇▅▃▂▁                │ │   ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁                    │
 │ rx 48.2KB/s                          │ │ rx 0B/s                              │
 ╰──────────────────────────────────────╯ ╰──────────────────────────────────────╯

    r refresh   ctrl+q quit
```

**实例（Instances）** —— 表格展示自启标志、状态、监听端口、peer 数量；
`u/d` 启停、`r` 重启、`b` 切换自启、`e` 编辑、`n` 新建、`x` 删除、
`enter` 查看实时 peer 详情。

```
 Instances
 ╭────────────────┬──────┬─────────┬───────┬───────┬────────┬──────────╮
 │ NAME           │ BOOT │ STATE   │ PORT  │ PEERS │ ONLINE │ UPTIME   │
 │ home           │  ★   │ up      │ 51821 │ 1     │ 1      │ 2h14m    │
 │ work           │  ★   │ up      │ 51820 │ 1     │ 1      │ 2h14m    │
 ╰────────────────┴──────┴─────────┴───────┴───────┴────────┴──────────╯
```

**日志（Logs）** —— 结构化事件（时间 · 级别 · 实例 · 消息），支持过滤
（`i` 实例、`l` 级别）和实时跟随（`f`）：

```
 Logs  [follow]
   inst:all  level:WARN  [i] inst  [l] level  [f] follow  [c] clear

 14:02:11  INFO   work    started on utun0 (pid 1234)
 14:03:40  WARN   home    endpoint vpn.example.com unresolved (will retry)
 14:05:02  INFO   work    endpoint resolved and pushed
 14:07:31  ERROR  home    instance unhealthy (process dead), cleaning up for restart
 14:07:32  INFO   home    started on utun7 (pid 1387)
```

**编辑器（Editor）** —— 接口与 peer 的完整表单：内联生成密钥、peer 增删
改、保存时校验（CIDR、密钥格式、以及**监听端口冲突**——与其他配置或系统
占用端口）。

**设置（Settings）** —— 切换界面语言（English / 中文，按用户持久化）、
查看 daemon 信息与各路径。

## 安装

```bash
brew trust dncore/tap           # 一次性：Homebrew 要求显式信任第三方 tap
brew install dncore/tap/wgtun   # 依赖 wireguard-go，自动安装
sudo wgtun daemon --install     # 安装并启动 launchd 守护进程
wgtun                           # 打开 TUI
```

配置位于 `/usr/local/etc/wireguard/*.conf`（wg-quick 兼容格式）。
首次启动自动导入已有文件，无需迁移。

## 导入现有配置

三种方式，按你手头有什么选：

```bash
wgtun import ~/Downloads/vpn.conf        # CLI：校验 → 开启自启 → 立即启动
wgtun import vpn.conf --name office      # 自定义实例名
```

- **TUI 内**：在「实例」页按 `n` 打开编辑器，按 `ctrl+v` 粘贴整份
  `[Interface]`/`[Peer]` 配置，`ctrl+s` 填充表单，调整后保存。
- **放文件**：把任意 `.conf` 放进 `/usr/local/etc/wireguard/`，daemon
  自动发现并导入、开启自启、立即启动。

daemon 启动时会检测 `wireguard-go`（路径与版本显示在设置页）。
缺失时启动日志与 TUI 会明确提示该装什么，而不是等第一条隧道启动失败。

## 架构

```
             你的用户（无需 sudo）                        root（launchd）

 ┌─────────────────────────────┐            ┌─────────────────────────────────────┐
 │  wgtun — 终端 UI            │            │  wgtun daemon — com.wgtun.daemon    │
 │                             │  HTTP      │                                     │
 │  Dashboard  Instances       │◄─经 unix───►│  监督器 / 收敛循环（每 5 秒）        │
 │  Editor     Logs  Settings  │  socket    │    ├── wireguard-go → utun0         │
 │                             │ /var/run/  │    ├── wireguard-go → utun7         │
 │  读取实时状态、编辑配置、    │ wgtun.sock │    └── ... 任意数量实例             │
 │  跟随日志                    │ (root:admin│                                     │
 │                             │  0660)     │  原生 UAPI：下发/查询配置            │
 └─────────────────────────────┘            │  ifconfig / route 配置地址路由      │
                                            │  收养 · 退避 · DNS 重解析           │
                                            │   ├─ 开机未就绪                     │
                                            │   └─ DDNS 变更（60s）               │
                                            │  /var/lib/wgtun/state.json          │
                                            │  /var/log/wgtun/events.jsonl        │
                                            └─────────────────────────────────────┘
```

- **守护进程独占进程树。** 每个实例是一个 `wireguard-go` 进程，带独立记账
  目录（`/var/run/wgtun/<name>/`：pid 文件 + 内核分配的 tun 名）。配置经原
  生 UAPI 协议下发；地址、MTU、路由用 `ifconfig`/`route` 配置，与
  wg-quick 完全一致。
- **自愈而非仅重启。** 收敛循环每 5 秒经 UAPI 探活每个运行中的实例：进程
  死亡或 socket 僵死会清理（杀进程 + 清运行时目录）并按退避重启（10 分钟
  内失败 5 次 → 暂停 5 分钟）。开机时 DNS 未就绪的端点稍后重解析并推送，走
  的是单 peer UAPI 写入：重试不会重置该实例其它健康 peer 的会话。
- **端点 DNS 会被持续跟随，而不是一次写死。** WireGuard 只在建立配置时解
  析一次端点，之后仅靠入向报文漫游（roaming），因此 DDNS 域名改指新地址
  后，隧道会一直打向当初记住的那个地址。收敛循环因此每 60s 重新解析所有
  `hostname:port` 端点，一旦解析结果变化就**就地**改写该 peer 的 endpoint
  （不带 `replace_peers`，会话与其它 peer 不受影响）。若某个 peer 的握手
  已陈旧 —— 这正是「域名指向了一个死地址」的特征 —— 则改为 15s 的快速
  通道，不必等满一个周期。配置里写成字面量 `ip:port` 的端点始终视为权威，
  永不重解析。请把解析记录的 TTL 控制在 60s 以内：系统解析器会按 TTL 缓存
  结果，这正是变更被感知速度的下限。
- **TTL 才是真正的下限。** 检测不可能快过解析记录的 TTL：daemon 与权威服务
  器之间的每一层解析器都会按 TTL 缓存旧答案（TTL 10 分钟就意味着最坏 ~11
  分钟才恢复，无论 daemon 查得多勤）。若服务商允许的最小 TTL 太长，只有两条
  出路：缩短 TTL，或者让「反向」不再依赖 DNS —— 在远端的、面向本机的那个
  peer 上加 `PersistentKeepalive = 25`，它的包会从新地址到达，WireGuard
  自身的 roaming 会在约 25s 内把我们的 endpoint 改过来，完全不涉及域名解析。
- **崩溃收养。** 守护进程自身崩溃时 wireguard-go 进程继续存活；下次启动按
  pid + socket 探活收养它们——守护进程重启不会中断隧道。前提是 plist 要求
  launchd 放过我们的进程组（`AbandonProcessGroup`，否则 `launchctl bootout`
  会把隧道一起带走）；代价是 `daemon --uninstall` 必须自己收尾，且只会对
  经 `ps` 确认仍是 wireguard-go 的 pid 发信号。
- **开机语义。** 启用自启的实例开机自动拉起。干净退出标记区分「整机重启」
  （应用自启）与「守护进程重启」（尊重你手动停止的状态）。
- **只有一个特权组件。** 你永远不需要 `sudo wgtun`；TUI 经 unix socket 与
  root 守护进程通信，配置文件保持 root 属主。

> 实现注记：wireguard-go 在 darwin 上会 fork 一次且启动器进程立即退出，
> 因此守护进程以 **UAPI socket 的持有者**（
> `lsof -t /var/run/wireguard/<tun>.sock`）为权威实例 pid，而非 `exec` 的
> 返回值。

## 配置

完整兼容 wg-quick 解析：多行 `Address`、`DNS`、`MTU`、`Table`、
`PostUp`/`PostDown`（在 up/down 时执行）。两处刻意的取舍：

- **DNS 会被解析和保留，但不应用到系统解析器** —— macOS 上全局改 DNS 侵
  入性大。需要的话用 `PostUp` 钩子自行处理。
- `Table` 接受但忽略。

在编辑器保存时做冲突检查：`ListenPort` 与其他 wgtun 实例冲突、或与系统
已占用端口冲突都会被拒绝（可强制保存绕过）。

## 守护进程运维

```bash
sudo wgtun daemon --install     # 安装并启动 com.wgtun.daemon
sudo wgtun daemon --uninstall   # 停止并移除
sudo wgtun daemon               # 前台运行（调试用）
sudo launchctl list | grep wgtun
sudo wg show all                # 依然可用
```

| 数据 | 位置 |
|---|---|
| 隧道配置 | `/usr/local/etc/wireguard/*.conf` |
| 期望状态（自启/停止） | `/var/lib/wgtun/state.json` |
| 运行时（socket、pid） | `/var/run/wgtun/` |
| 事件日志 | `/var/log/wgtun/events.jsonl`（自动轮转） |
| TUI 设置 | `~/.config/wgtun/settings.json` |

## 发布

推送 `v*` tag 触发发布流水线：跑测试、构建 darwin/amd64 + arm64、创建
GitHub Release，并把 `Formula/wgtun.rb` 推到 `dncore/homebrew-tap`。

**一次性配置**（本仓库已完成）：

1. 创建 tap 仓库：
   `gh repo create dncore/homebrew-tap --public --add-readme`
2. 创建 fine-grained PAT（**Contents: Read and write**，仓库范围仅限
   `dncore/homebrew-tap`），存入 secret：
   `gh secret set HOMEBREW_TAP_TOKEN --repo dncore/wgtun`
   （默认的 `GITHUB_TOKEN` 无法向其他仓库推送。）

**每次发版：**

```bash
git tag v0.2.0
git push origin v0.2.0
```

用户升级：`brew update && brew upgrade wgtun`。若 LaunchDaemon 是用
`wgtun daemon --install` 安装的，从 0.2.1 及更早版本升级后需重跑一次：那些
版本会把带版本号的 `Cellar/wgtun/<version>/bin/wgtun` 路径写进 plist，而
`brew upgrade` 会删除该目录（0.2.2 起写入稳定的 `bin/wgtun` 符号链接）。

## 故障排查

| 现象 | 检查 |
|---|---|
| TUI 显示 `daemon offline` | `sudo wgtun daemon --install`；socket 位于 `/var/run/wgtun.sock` |
| `brew upgrade` 后 daemon 不再启动 | `sudo wgtun daemon --install` —— 0.2.1 及更早版本会把带版本号的 `Cellar/...` 路径写进 plist |
| 实例已启动但无握手 | 日志页或 `/var/log/wgtun/events.jsonl`；检查端点 DNS |
| 实例反复重启 | 日志页有原因；连续 5 次失败后会自动退避 |
| 日志出现 `still absent from the device ... giving up` | 该 peer 的公钥与本实例自己的公钥相同；wireguard-go 会返回 `errno=0` 但静默丢弃这个 peer |
| 开机时 DNS 未就绪，某个 peer 迟迟没有端点 | 预期行为：daemon 会持续重试单 peer 推送，解析器可用前 `unresolved (will retry)` 警告会反复出现 |
| 对端 IP 变更后隧道断掉且日志没有 `dns changed` 事件 | DDNS 记录 TTL 大于 60s，解析器仍在返回旧答案，请调低 TTL。若配置里端点写成字面量 `ip:port`，按设计不会被重解析 |
| 保存时报端口冲突 | 编辑器会指出冲突的实例名或宿主进程 |

## 开发

```bash
go build -o wgtun ./cmd/wgtun
sudo env WGTUN_CONF_DIR=$PWD/conf ./wgtun daemon   # 沙箱运行

go test ./...           # 单元 + 集成测试
```

所有路径都可用 `WGTUN_*` 环境变量覆盖（见 `internal/paths`），守护进程可
在测试中完全无特权运行：`WGTUN_CONF_DIR WGTUN_STATE_DIR WGTUN_RUN_DIR
WGTUN_LOG_DIR WGTUN_SOCK WGTUN_WIREGUARD_GO`。

## 从 v1 shell 体系迁移

早期版本部署了每接口一份的 launchd plist（`com.wireguard.*`）、
`healthcheck.sh` 和 sleepwatcher 钩子。运行 wgtun 前先停掉它们——两者会同
时争抢同一批隧道：

```bash
# 1. 停止旧任务并删除 plist
for l in com.wireguard.wg0 com.wireguard.wg1 com.wireguard.healthcheck com.wireguard.ui; do
  sudo launchctl bootout system/$l 2>/dev/null; done
sudo rm -f /Library/LaunchDaemons/com.wireguard.*.plist
brew services stop sleepwatcher

# 2. 杀掉 v1 残留进程和运行时文件
sudo pkill -f 'wg-quick up wg'; sudo pkill -f 'wireguard-go utun'
sudo rm -rf /var/run/wireguard

# 3. 启动新世界（配置原样复用）
sudo wgtun daemon --install
wgtun
```

## License

MIT
