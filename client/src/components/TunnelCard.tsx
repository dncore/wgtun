import type { Tunnel } from "../types"
import { Activity, ArrowDown, ArrowUp, Globe, Key, Radio } from "lucide-react"

function Badge({ variant, children }: { variant: "success" | "muted"; children: React.ReactNode }) {
  const bg = variant === "success" ? "bg-green-50 text-green-700" : "bg-gray-100 text-gray-500"
  return <span className={`inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium ${bg}`}>{children}</span>
}

export function TunnelCard({ tunnel }: { tunnel: Tunnel }) {
  const peer = tunnel.peers[0]
  if (!peer) return null

  const isConnected = peer.handshake !== "0 seconds ago" && !peer.handshake.includes("Never")

  return (
    <div className="rounded-lg border bg-white p-5 shadow-sm">
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <div className={`w-2 h-2 rounded-full ${isConnected ? "bg-green-500" : "bg-gray-300"}`} />
          <h3 className="font-semibold text-sm text-gray-900">{tunnel.interface}</h3>
        </div>
        <Badge variant={isConnected ? "success" : "muted"}>
          {isConnected ? "已连接" : "未连接"}
        </Badge>
      </div>

      <div className="grid grid-cols-2 gap-3 text-sm">
        <div className="flex items-center gap-2 text-gray-500">
          <Globe className="w-3.5 h-3.5" />
          <span className="truncate text-xs">{peer.endpoint}</span>
        </div>
        <div className="flex items-center gap-2 text-gray-500">
          <Key className="w-3.5 h-3.5" />
          <span className="truncate text-xs font-mono">{peer.publicKey.slice(0, 16)}…</span>
        </div>
        <div className="flex items-center gap-2 text-gray-500">
          <Radio className="w-3.5 h-3.5" />
          <span className="text-xs">{tunnel.listeningPort}</span>
        </div>
        <div className="flex items-center gap-2 text-gray-500">
          <Activity className="w-3.5 h-3.5" />
          <span className="text-xs">{peer.handshake}</span>
        </div>
      </div>

      <div className="flex gap-4 mt-4 pt-3 border-t border-gray-100">
        <div className="flex items-center gap-1.5 text-xs text-gray-500">
          <ArrowDown className="w-3 h-3 text-green-500" />
          <span>{peer.transferRx}</span>
        </div>
        <div className="flex items-center gap-1.5 text-xs text-gray-500">
          <ArrowUp className="w-3 h-3 text-blue-500" />
          <span>{peer.transferTx}</span>
        </div>
        <div className="flex-1" />
        <span className="text-xs text-gray-400 font-mono">{peer.allowedIps}</span>
      </div>
    </div>
  )
}
