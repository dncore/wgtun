import { useEffect, useRef, useState } from "react"
import type { LogFile } from "../types"

function colorLine(line: string): string {
  const lower = line.toLowerCase()
  if (/\berror\b|\bfail\b|\bfatal\b|\bpanic\b|\bkilled\b/.test(lower)) return "text-red-400"
  if (/\bwarn\b|\btimeout\b|\bversion mismatch\b/.test(lower)) return "text-yellow-400"
  if (/^\[#\]|^\[\+\]/.test(line)) return "text-blue-400"
  return "text-gray-400"
}

export function LogViewer({ logs, onClear }: { logs: LogFile[]; onClear: () => void }) {
  const [active, setActive] = useState<string | null>(null)
  const [confirm, setConfirm] = useState(false)
  const topRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!active) {
      const first = logs.find((l) => l.size > 0)
      if (first) setActive(first.name)
    }
  }, [logs, active])

  useEffect(() => {
    topRef.current?.scrollIntoView({ behavior: "instant" })
  }, [active])

  const activeLog = logs.find((l) => l.name === active)
  const lines = activeLog?.content
    ? activeLog.content.split("\n").reverse()
    : []

  const hasContent = logs.some((l) => l.size > 0)

  const doClear = async () => {
    const res = await fetch("/api/logs/clear", { method: "POST" })
    if (res.status === 401) { window.location.href = "/login.html"; return }
    setConfirm(false)
    onClear()
  }

  return (
    <div className="rounded-lg border bg-[#0d1117] shadow-sm overflow-hidden relative">
      {/* Tabs */}
      <div className="flex border-b border-gray-800 overflow-x-auto items-center scrollbar-none" style={{scrollbarWidth:"none"}}>
        {logs.map((log) => (
          <button
            key={log.name}
            onClick={() => setActive(log.name)}
            className={`flex items-center gap-1.5 px-4 py-2 text-xs font-medium whitespace-nowrap transition-colors border-b-2 -mb-px ${
              active === log.name
                ? "text-gray-100 border-blue-500 bg-blue-500/10"
                : "text-gray-500 border-transparent hover:text-gray-300 hover:bg-gray-800/50"
            }`}
          >
            {log.name.replace(/\.(err|out)\.log$/, "")}
            <span className={`text-[10px] ${log.size > 0 ? "text-gray-400" : "text-gray-600"}`}>
              {log.size > 0 ? `${(log.size / 1024).toFixed(1)}KB` : "空"}
            </span>
          </button>
        ))}
      </div>

      {/* Confirmation modal */}
      {confirm && (
        <div className="absolute inset-0 z-10 bg-[#0d1117]/90 flex items-center justify-center">
          <div className="bg-gray-900 border border-gray-700 rounded-lg p-6 shadow-2xl max-w-xs w-full">
            <p className="text-sm text-gray-200 mb-1">确认清空全部日志？</p>
            <p className="text-xs text-gray-500 mb-4">此操作不可撤销</p>
            <div className="flex gap-2 justify-end">
              <button
                onClick={() => setConfirm(false)}
                className="px-3 py-1.5 text-xs text-gray-400 hover:text-gray-200 transition-colors"
              >
                取消
              </button>
              <button
                onClick={doClear}
                className="px-3 py-1.5 text-xs font-medium bg-red-600 text-white rounded hover:bg-red-500 transition-colors"
              >
                确认清空
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Clear bar */}
      {hasContent && (
        <div className="flex justify-end px-2 py-1 border-b border-gray-800">
          <button
            onClick={() => setConfirm(true)}
            className="px-2 py-0.5 text-[10px] text-gray-500 hover:text-red-400 transition-colors"
          >
            清空全部日志
          </button>
        </div>
      )}

      {/* Content */}
      <div
        className="overflow-y-auto font-mono text-xs leading-relaxed"
        style={{ maxHeight: "360px" }}
      >
        {!activeLog || lines.length === 0 ? (
          <div className="flex items-center justify-center py-16 text-gray-600 text-xs">
            {activeLog ? "（空）" : "选择一个日志文件"}
          </div>
        ) : (
          <div className="py-3">
            <div ref={topRef} />
            {lines.map((line, i) => (
              <div
                key={i}
                className="flex hover:bg-gray-800/30 transition-colors"
              >
                <span className="flex-shrink-0 w-12 text-right pr-3 text-gray-700 select-none">
                  {lines.length - i}
                </span>
                <span className={`${colorLine(line)} break-all pr-4`}>
                  {line || " "}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
