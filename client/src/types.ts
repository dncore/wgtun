export interface Peer {
  publicKey: string
  endpoint: string
  allowedIps: string
  handshake: string
  transferRx: string
  transferTx: string
}

export interface Tunnel {
  interface: string
  publicKey: string
  listeningPort: string
  peers: Peer[]
}

export interface ConfigFile {
  name: string
  content: string
}

export interface LogFile {
  name: string
  content: string
  size: number
}

export interface Status {
  tunnels: Tunnel[]
  configs: ConfigFile[]
  logs: LogFile[]
  uptime: string
}
