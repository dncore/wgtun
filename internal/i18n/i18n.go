// Package i18n provides the en/zh string tables used by the TUI.
// Default language is English; the user can switch at runtime (Settings tab),
// persisted to the settings file.
package i18n

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dncore/wgtun/internal/paths"
)

// Lang is a supported UI language.
type Lang string

const (
	En Lang = "en"
	Zh Lang = "zh"
)

// Keys — every translatable string in the TUI.
const (
	TabDashboard = "tab.dashboard"
	TabInstances = "tab.instances"
	TabLogs      = "tab.logs"
	TabSettings  = "tab.settings"

	// dashboard
	DashboardTitle  = "dashboard.title"
	DashboardSub    = "dashboard.subtitle"
	StatRunning     = "stat.running"
	StatTotal       = "stat.total"
	StatOnline      = "stat.online"
	StatTraffic     = "stat.traffic"
	CardUp          = "card.up"
	CardDown        = "card.down"
	CardHandshake   = "card.handshake"
	CardRx          = "card.rx"
	CardTx          = "card.tx"
	CardPeers       = "card.peers"
	CardEndpoint    = "card.endpoint"
	CardTun         = "card.tun"
	CardListen      = "card.listen"
	CardPubKey      = "card.pubKey"
	CardAllowed     = "card.allowed"
	CardNone        = "card.none"
	Never           = "time.never"
	HandshakeNow    = "time.handshakeNow"
	HandshakeAgo    = "time.handshakeAgo" // args: duration
	NoInstances     = "dashboard.noInstances"
	WgGoMissing     = "settings.wireguardGoMissing"
	DaemonOffline   = "daemon.offline"
	DaemonReconnecting = "daemon.reconnecting"

	// instances
	InstTitle       = "inst.title"
	InstName        = "inst.name"
	InstAutostart   = "inst.autostart"
	InstState       = "inst.state"
	InstPort        = "inst.port"
	InstPeers       = "inst.peers"
	InstOnline      = "inst.online"
	InstUptime      = "inst.uptime"
	InstRunning     = "state.running"
	InstStopped     = "state.stopped"
	InstConflict    = "inst.conflict"
	InstNoConflict  = "inst.noConflict"
	KeyStartStop    = "key.startStop"
	KeyRestart      = "key.restart"
	KeyEdit         = "key.edit"
	KeyDelete       = "key.delete"
	KeyNew          = "key.new"
	KeyRefresh      = "key.refresh"
	KeyToggleBoot   = "key.toggleBoot"
	KeyDetails      = "key.details"
	ConfirmDelete   = "inst.confirmDelete" // args: name
	Deleted         = "inst.deleted"
	Started         = "inst.started"
	Stopped         = "inst.stopped"
	Restarted       = "inst.restarted"
	BootEnabled     = "inst.bootEnabled"
	BootDisabled    = "inst.bootDisabled"
	ActionFailed    = "inst.actionFailed" // args: err
	InstanceOffline = "inst.offlineDetail"

	// editor
	EditorTitle       = "editor.title"
	EditorNewTitle    = "editor.newTitle"
	FieldName         = "editor.fieldName"
	FieldPrivateKey   = "editor.fieldPrivateKey"
	FieldAddress      = "editor.fieldAddress"
	FieldListenPort   = "editor.fieldListenPort"
	FieldMTU          = "editor.fieldMTU"
	FieldDNS          = "editor.fieldDNS"
	FieldPeerPublic   = "editor.fieldPeerPublic"
	FieldPeerAllowed  = "editor.fieldPeerAllowed"
	FieldPeerEndpoint = "editor.fieldPeerEndpoint"
	FieldPeerKeepalive = "editor.fieldPeerKeepalive"
	FieldPeerPSK      = "editor.fieldPeerPSK"
	KeyGenKey         = "key.genKey"
	KeyAddPeer        = "key.addPeer"
	KeyDelPeer        = "key.delPeer"
	KeySave           = "key.save"
	KeyCancel         = "key.cancel"
	KeyForce          = "key.force"
	EditorSaved       = "editor.saved"
	EditorSavedRestart = "editor.savedNeedRestart"
	EditorError       = "editor.error"
	Peers             = "editor.peers"
	PeerListHint      = "editor.peerHint"
	ForceHint         = "editor.forceHint"
	ConflictFound     = "editor.conflictFound" // args: err
	NoPeers           = "editor.noPeers"

	// logs
	LogsTitle       = "logs.title"
	LogsAll         = "logs.all"
	LogsFollow      = "logs.follow"
	LogsPause       = "logs.pause"
	LogsFilter      = "logs.filter"
	LogsNoEvents    = "logs.noEvents"
	KeyFollow       = "key.follow"
	KeyClearFilter  = "key.clearFilter"
	KeyPaste        = "key.paste"
	PasteHint       = "editor.pasteHint"
	FilterCleared   = "logs.filterCleared"
	LevelDebug      = "level.debug"
	LevelInfo       = "level.info"
	LevelWarn       = "level.warn"
	LevelError      = "level.error"

	// settings
	SettingsTitle     = "settings.title"
	SettingsLang      = "settings.language"
	SettingsLangDesc  = "settings.languageDesc"
	LangEn            = "lang.en"
	LangZh            = "lang.zh"
	SettingsDaemon    = "settings.daemon"
	SettingsDaemonOff = "settings.daemonOffline"
	SettingsVersion   = "settings.version"
	SettingsWgGo      = "settings.wireguardGo"
	SettingsUptime    = "settings.uptime"
	SettingsSocket    = "settings.socket"
	SettingsConfDir   = "settings.confDir"
	SettingsRunDir    = "settings.runDir"
	SettingsLogDir    = "settings.logDir"
	KeyQuit           = "key.quit"
	KeySwitchLang     = "key.switchLang"
	KeyHelp           = "key.help"

	// status line
	StatusBusy      = "status.busy"
	StatusDone      = "status.done"

	// fmt helpers
	BytesHuman = "fmt.bytes" // done in code, no key needed
)

var tables = map[Lang]map[string]string{
	En: {
		TabDashboard: "Dashboard", TabInstances: "Instances", TabLogs: "Logs", TabSettings: "Settings",
		DashboardTitle: "WireGuard Dashboard", DashboardSub: "wireguard-go orchestrator",
		StatRunning: "Running", StatTotal: "Total", StatOnline: "Online peers", StatTraffic: "Total traffic",
		CardUp: "UP", CardDown: "DOWN", CardHandshake: "handshake", CardRx: "rx", CardTx: "tx",
		CardPeers: "peers", CardEndpoint: "endpoint", CardTun: "tun",
		CardListen: "listen", CardPubKey: "pub", CardAllowed: "allowed", CardNone: "—",
		Never: "never", HandshakeNow: "just now", HandshakeAgo: "%s ago",
		NoInstances: "No instances yet — press n in the Instances tab to create one.\nIn the editor, ctrl+v pastes an existing WireGuard config; alternatively drop\nany .conf file into the WireGuard config directory and it is imported automatically.",
		WgGoMissing: "wireguard-go not found — install it with: brew install wireguard-go",
		DaemonOffline: "daemon offline", DaemonReconnecting: "reconnecting…",
		InstTitle: "Instances", InstName: "NAME", InstAutostart: "BOOT", InstState: "STATE", InstPort: "PORT",
		InstPeers: "PEERS", InstOnline: "ONLINE", InstUptime: "UPTIME",
		InstRunning: "running", InstStopped: "stopped",
		InstConflict: "⚠ port conflict", InstNoConflict: "—",
		KeyStartStop: "start/stop", KeyRestart: "restart", KeyEdit: "edit", KeyDelete: "delete",
		KeyNew: "new", KeyRefresh: "refresh", KeyToggleBoot: "toggle boot", KeyDetails: "details",
		ConfirmDelete: "Delete %s? (y/n)", Deleted: "deleted", Started: "started", Stopped: "stopped",
		Restarted: "restarted", BootEnabled: "boot autostart ON", BootDisabled: "boot autostart OFF",
		ActionFailed: "action failed: %s", InstanceOffline: "(instance offline)",
		EditorTitle: "Edit instance", EditorNewTitle: "New instance",
		FieldName: "Name", FieldPrivateKey: "Private key", FieldAddress: "Addresses",
		FieldListenPort: "Listen port", FieldMTU: "MTU", FieldDNS: "DNS",
		FieldPeerPublic: "Peer public key", FieldPeerAllowed: "Allowed IPs",
		FieldPeerEndpoint: "Endpoint", FieldPeerKeepalive: "Keepalive (s)", FieldPeerPSK: "Preshared key",
		KeyGenKey: "generate key", KeyAddPeer: "add peer", KeyDelPeer: "delete peer",
		KeySave: "save", KeyCancel: "cancel", KeyForce: "force save",
		EditorSaved: "saved", EditorSavedRestart: "saved — restart the instance to apply",
		EditorError: "error", Peers: "Peers", PeerListHint: "up/down select · e edit · n new · d delete",
		ForceHint: "force ignores port conflicts", ConflictFound: "conflict: %s", NoPeers: "no peers",
		LogsTitle: "Logs", LogsAll: "all", LogsFollow: "follow", LogsPause: "paused",
		LogsFilter: "filter", LogsNoEvents: "no events match",
		KeyFollow: "follow/pause", KeyClearFilter: "clear filter",
		KeyPaste:      "ctrl+v paste config",
		PasteHint:     "Paste a full WireGuard config ([Interface] + [Peer] sections), then ctrl+s applies it to the form. esc cancels.",
		FilterCleared: "filters cleared, reloaded",
		LevelDebug: "DEBUG", LevelInfo: "INFO", LevelWarn: "WARN", LevelError: "ERROR",
		SettingsTitle: "Settings", SettingsLang: "Language", SettingsLangDesc: "switch UI language",
		LangEn: "English", LangZh: "中文",
		SettingsDaemon: "Daemon", SettingsDaemonOff: "daemon offline — start it with: sudo wgtun daemon --install",
		SettingsVersion: "Version", SettingsWgGo: "wireguard-go", SettingsUptime: "Uptime", SettingsSocket: "Socket",
		SettingsConfDir: "Config dir", SettingsRunDir: "Run dir", SettingsLogDir: "Log dir",
		KeyQuit: "quit", KeySwitchLang: "switch language", KeyHelp: "help",
		StatusBusy: "working…", StatusDone: "done",
	},
	Zh: {
		TabDashboard: "仪表盘", TabInstances: "实例", TabLogs: "日志", TabSettings: "设置",
		DashboardTitle: "WireGuard 仪表盘", DashboardSub: "wireguard-go 编排器",
		StatRunning: "运行中", StatTotal: "总数", StatOnline: "在线 peer", StatTraffic: "总流量",
		CardUp: "运行", CardDown: "停止", CardHandshake: "握手", CardRx: "收", CardTx: "发",
		CardPeers: "peer", CardEndpoint: "端点", CardTun: "接口",
		CardListen: "监听", CardPubKey: "公钥", CardAllowed: "允许", CardNone: "—",
		Never: "从未", HandshakeNow: "刚刚", HandshakeAgo: "%s 前",
		NoInstances: "还没有实例 —— 在「实例」页按 n 新建。\n编辑器里 ctrl+v 可粘贴现有的 WireGuard 配置；\n或把任意 .conf 文件放进 WireGuard 配置目录，会自动导入。",
		WgGoMissing: "未找到 wireguard-go —— 请安装: brew install wireguard-go",
		DaemonOffline: "daemon 离线", DaemonReconnecting: "重连中…",
		InstTitle: "实例", InstName: "名称", InstAutostart: "自启", InstState: "状态", InstPort: "端口",
		InstPeers: "peer", InstOnline: "在线", InstUptime: "运行时长",
		InstRunning: "运行中", InstStopped: "已停止",
		InstConflict: "⚠ 端口冲突", InstNoConflict: "—",
		KeyStartStop: "启动/停止", KeyRestart: "重启", KeyEdit: "编辑", KeyDelete: "删除",
		KeyNew: "新建", KeyRefresh: "刷新", KeyToggleBoot: "切换自启", KeyDetails: "详情",
		ConfirmDelete: "删除 %s？(y/n)", Deleted: "已删除", Started: "已启动", Stopped: "已停止",
		Restarted: "已重启", BootEnabled: "开机自启 已开启", BootDisabled: "开机自启 已关闭",
		ActionFailed: "操作失败: %s", InstanceOffline: "(实例离线)",
		EditorTitle: "编辑实例", EditorNewTitle: "新建实例",
		FieldName: "名称", FieldPrivateKey: "私钥", FieldAddress: "地址",
		FieldListenPort: "监听端口", FieldMTU: "MTU", FieldDNS: "DNS",
		FieldPeerPublic: "Peer 公钥", FieldPeerAllowed: "允许 IP",
		FieldPeerEndpoint: "端点", FieldPeerKeepalive: "Keepalive(秒)", FieldPeerPSK: "预共享密钥",
		KeyGenKey: "生成密钥", KeyAddPeer: "添加 peer", KeyDelPeer: "删除 peer",
		KeySave: "保存", KeyCancel: "取消", KeyForce: "强制保存",
		EditorSaved: "已保存", EditorSavedRestart: "已保存 — 重启实例后生效",
		EditorError: "错误", Peers: "Peer 列表", PeerListHint: "↑↓ 选择 · e 编辑 · n 新增 · d 删除",
		ForceHint: "强制保存会忽略端口冲突", ConflictFound: "冲突: %s", NoPeers: "无 peer",
		LogsTitle: "日志", LogsAll: "全部", LogsFollow: "跟随", LogsPause: "已暂停",
		LogsFilter: "过滤", LogsNoEvents: "没有匹配的事件",
		KeyFollow: "跟随/暂停", KeyClearFilter: "清除过滤",
		KeyPaste:      "ctrl+v 粘贴配置",
		PasteHint:     "粘贴完整 WireGuard 配置（[Interface] + [Peer] 段），ctrl+s 应用到表单，esc 取消。",
		FilterCleared: "过滤器已清除，已重新加载",
		LevelDebug: "调试", LevelInfo: "信息", LevelWarn: "警告", LevelError: "错误",
		SettingsTitle: "设置", SettingsLang: "语言", SettingsLangDesc: "切换界面语言",
		LangEn: "English", LangZh: "中文",
		SettingsDaemon: "Daemon", SettingsDaemonOff: "daemon 离线 — 用 sudo wgtun daemon --install 启动",
		SettingsVersion: "版本", SettingsWgGo: "wireguard-go", SettingsUptime: "运行时长", SettingsSocket: "Socket",
		SettingsConfDir: "配置目录", SettingsRunDir: "运行目录", SettingsLogDir: "日志目录",
		KeyQuit: "退出", KeySwitchLang: "切换语言", KeyHelp: "帮助",
		StatusBusy: "处理中…", StatusDone: "完成",
	},
}

// T returns a localized string with fmt-style args.
func T(lang Lang, key string, args ...any) string {
	table, ok := tables[lang]
	if !ok {
		table = tables[En]
	}
	s, ok := table[key]
	if !ok {
		s = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// IsValid reports whether lang is a supported language.
func IsValid(l Lang) bool { _, ok := tables[l]; return ok }

// ---- persistence ----

// Settings holds persisted TUI settings.
type Settings struct {
	Lang Lang `json:"lang"`
}

// LoadSettings reads the TUI settings file (default English).
func LoadSettings() Settings {
	s := Settings{Lang: En}
	data, err := os.ReadFile(paths.SettingsFile())
	if err != nil {
		return s
	}
	json.Unmarshal(data, &s)
	if !IsValid(s.Lang) {
		s.Lang = En
	}
	return s
}

// Save persists the TUI settings file.
func (s Settings) Save() error {
	dir := paths.SettingsFile()
	// mkdir the parent (~/.config/wgtun)
	parent := dir[:len(dir)-len("/settings.json")]
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dir, data, 0o600)
}