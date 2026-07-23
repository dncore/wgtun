import { useEffect, useRef, useState, useCallback } from "react"
import { RefreshCw, Shield, Play } from "lucide-react"
import type { Status } from "./types"
import { TunnelCard } from "./components/TunnelCard"
import { ConfigPanel } from "./components/ConfigPanel"
import { LogViewer } from "./components/LogViewer"

const POLL_INTERVAL = 5000
const IDLE_TIMEOUT = 5 * 60 * 1000 // 5 分钟无操作 → 暂停

function App() {
  const [status, setStatus] = useState<Status | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [lastRefresh, setLastRefresh] = useState<number>(0)
  const [paused, setPaused] = useState(false)
  const lastActivity = useRef(Date.now())
  const idleTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const fetchStatus = useCallback(async () => {
    if (paused) return
    try {
      const res = await fetch("/api/status")
      if (res.status === 401) { window.location.href = "/login.html"; return }
      if (!res.ok) throw new Error(res.statusText)
      const data = await res.json()
      setStatus(data)
      setError(null)
      setLastRefresh(Date.now())
    } catch (e) {
      setError(e instanceof Error ? e.message : "连接失败")
    }
  }, [paused])

  // reset idle timer on user interaction
  const resetIdle = useCallback(() => {
    lastActivity.current = Date.now()
    setPaused(false)
    if (idleTimer.current) {
      clearTimeout(idleTimer.current)
      idleTimer.current = null
    }
    // schedule next idle check
    idleTimer.current = setTimeout(() => {
      setPaused(true)
    }, IDLE_TIMEOUT)
  }, [])

  // polling: pauses when tab hidden or idle
  useEffect(() => {
    fetchStatus()

    const pollTimer = setInterval(() => {
      const hidden = document.hidden
      const idle = Date.now() - lastActivity.current > IDLE_TIMEOUT
      if (!hidden && !idle) {
        fetchStatus()
      } else if (!paused) {
        setPaused(true)
      }
    }, POLL_INTERVAL)

    const onVisible = () => {
      if (document.hidden) {
        setPaused(true)
      } else {
        resetIdle()
      }
    }
    document.addEventListener("visibilitychange", onVisible)

    // user activity events
    const events = ["mousemove", "keydown", "click", "scroll", "touchstart"]
    events.forEach((e) => document.addEventListener(e, resetIdle, { passive: true }))

    // start idle timer
    idleTimer.current = setTimeout(() => setPaused(true), IDLE_TIMEOUT)

    return () => {
      clearInterval(pollTimer)
      if (idleTimer.current) clearTimeout(idleTimer.current)
      document.removeEventListener("visibilitychange", onVisible)
      events.forEach((e) => document.removeEventListener(e, resetIdle))
    }
  }, [fetchStatus, resetIdle, paused])

  const resume = () => {
    resetIdle()
    fetchStatus()
  }

  if (error && !status) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[60vh] gap-3 text-gray-400">
        <Shield className="w-8 h-8" />
        <p className="text-sm">无法连接服务 — 请确认后端已启动</p>
        <p className="text-xs text-gray-300">{error}</p>
        <button
          onClick={fetchStatus}
          className="mt-2 px-4 py-1.5 text-xs font-medium bg-gray-100 rounded-md hover:bg-gray-200 transition-colors"
        >
          重试
        </button>
      </div>
    )
  }

  if (!status) {
    return (
      <div className="flex justify-center py-20 text-gray-400 text-sm">加载中…</div>
    )
  }

  const connectedCount = status.tunnels.filter(
    (t) => t.peers[0] && !t.peers[0].handshake.includes("Never")
  ).length

  return (
    <div className="relative space-y-6">
      {/* Pause overlay */}
      {paused && (
        <div
          className="fixed inset-0 z-50 bg-white/70 backdrop-blur-sm flex flex-col items-center justify-center cursor-pointer"
          onClick={resume}
        >
          <div className="flex flex-col items-center gap-3 p-8 rounded-xl bg-white border shadow-lg">
            <Play className="w-10 h-10 text-gray-400" />
            <p className="text-sm text-gray-500">数据刷新已暂停</p>
            <p className="text-xs text-gray-300">（标签页不可见或无操作超时）</p>
            <span className="mt-1 px-4 py-1.5 text-xs font-medium bg-gray-900 text-white rounded-md">
              点击恢复刷新
            </span>
          </div>
        </div>
      )}

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-gray-900 tracking-tight">WireGuard</h1>
          <p className="text-sm text-gray-500 mt-0.5">
            {connectedCount}/{status.tunnels.length} 隧道在线
            <span className="mx-2 text-gray-300">·</span>
            <button onClick={fetchStatus} className="inline-flex items-center gap-1 text-xs text-gray-400 hover:text-gray-600 transition-colors">
              <RefreshCw className="w-3 h-3" />
              {Math.floor((Date.now() - lastRefresh) / 1000)}s 前刷新
            </button>
          </p>
        </div>
      </div>

      {/* Tunnels */}
      {status.tunnels.length === 0 ? (
        <div className="rounded-lg border bg-white p-12 text-center text-sm text-gray-400">
          未检测到活跃隧道 — 请确认 WireGuard 服务已启动
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {status.tunnels.map((tunnel) => (
            <TunnelCard key={tunnel.interface} tunnel={tunnel} />
          ))}
        </div>
      )}

      {/* Configs */}
      {status.configs.length > 0 && (
        <div>
          <h2 className="text-sm font-semibold text-gray-500 mb-3 uppercase tracking-wider">配置</h2>
          <ConfigPanel configs={status.configs} />
        </div>
      )}

      {/* Logs */}
      <div>
        <h2 className="text-sm font-semibold text-gray-500 mb-3 uppercase tracking-wider">日志</h2>
        <LogViewer logs={status.logs} onClear={fetchStatus} />
      </div>

      {/* Footer */}
      <div className="text-center text-xs text-gray-300 pt-4">
        {status.uptime}
      </div>
    </div>
  )
}

export default App
