import { useState } from "react"
import type { ConfigFile } from "../types"

export function ConfigPanel({ configs }: { configs: ConfigFile[] }) {
  const [active, setActive] = useState(configs[0]?.name ?? null)
  const cfg = configs.find((c) => c.name === active)

  return (
    <div className="rounded-lg border bg-white shadow-sm overflow-hidden flex" style={{ minHeight: 200 }}>
      {/* left tabs */}
      <div className="w-36 flex-shrink-0 border-r bg-gray-50/50">
        {configs.map((c) => (
          <button
            key={c.name}
            onClick={() => setActive(c.name)}
            className={`w-full text-left px-4 py-2.5 text-xs font-medium transition-colors border-l-2 ${
              active === c.name
                ? "text-gray-900 bg-white border-blue-500"
                : "text-gray-500 border-transparent hover:text-gray-700 hover:bg-gray-50"
            }`}
          >
            {c.name}
          </button>
        ))}
      </div>

      {/* right content */}
      <div className="flex-1 min-w-0">
        {cfg ? (
          <pre className="text-xs text-gray-600 p-5 leading-relaxed font-mono overflow-x-auto whitespace-pre-wrap">
            {cfg.content}
          </pre>
        ) : (
          <div className="flex items-center justify-center h-full text-xs text-gray-400">
            选择一个配置文件
          </div>
        )}
      </div>
    </div>
  )
}
