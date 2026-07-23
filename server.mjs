#!/usr/bin/env node
/**
 * WireGuard Dashboard — Node.js 后端（备选实现）
 * 使用方式: node server.mjs
 * 或通过 package.json: npm start
 */
import express from "express";
import { execFile } from "child_process";
import { readFile, readdir, writeFileSync, readFileSync, existsSync } from "fs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// ---- 配置：环境变量 > 默认值 ----
const PORT = parseInt(process.env.UI_PORT || "4623", 10);
const WG_BIN = process.env.WG_BIN || "/opt/homebrew/opt/wireguard-tools/bin/wg";
const CONFIG_DIR = process.env.CONFIG_DIR || "/usr/local/etc/wireguard";
const LOG_DIR = process.env.LOG_DIR || "/var/log/wireguard";
const UPTIME_FILE = "/tmp/wireguard-ui-start";

const app = express();
app.use(express.json());

// ---- helpers ----

function runWg() {
  return new Promise((resolve) => {
    execFile("sudo", [WG_BIN, "show", "all"], { timeout: 5000 }, (err, stdout) => {
      resolve(err ? "" : stdout);
    });
  });
}

function parseWgOutput(raw) {
  const tunnels = [];
  let current = null;
  let currentPeer = null;

  for (const line of raw.split("\n")) {
    const im = line.match(/^interface:\s+(.+)/);
    if (im) {
      if (current) tunnels.push(current);
      current = { interface: im[1], publicKey: "", listeningPort: "", peers: [] };
      currentPeer = null;
      continue;
    }
    if (!current) continue;

    const pkm = line.match(/^\s+public key:\s+(.+)/);
    if (pkm) { current.publicKey = pkm[1]; continue; }

    const lpm = line.match(/^\s+listening port:\s+(.+)/);
    if (lpm) { current.listeningPort = lpm[1]; continue; }

    const prm = line.match(/^peer:\s+(.+)/);
    if (prm) {
      currentPeer = { publicKey: prm[1], endpoint: "", allowedIps: "", handshake: "", transferRx: "", transferTx: "" };
      current.peers.push(currentPeer);
      continue;
    }
    if (!currentPeer) continue;

    const epm = line.match(/^\s+endpoint:\s+(.+)/);
    if (epm) { currentPeer.endpoint = epm[1]; continue; }

    const aim = line.match(/^\s+allowed ips:\s+(.+)/);
    if (aim) { currentPeer.allowedIps = aim[1]; continue; }

    const hm = line.match(/^\s+latest handshake:\s+(.+)/);
    if (hm) { currentPeer.handshake = hm[1]; continue; }

    const tm = line.match(/^\s+transfer:\s+(.+)\s+received,\s+(.+)\s+sent/);
    if (tm) { currentPeer.transferRx = tm[1]; currentPeer.transferTx = tm[2]; continue; }
  }
  if (current) tunnels.push(current);
  return tunnels;
}

async function readConfigs() {
  const configs = [];
  if (!existsSync(CONFIG_DIR)) return configs;
  const files = await readdir(CONFIG_DIR);
  for (const f of files.filter((f) => f.endsWith(".conf")).sort()) {
    const content = await readFile(path.join(CONFIG_DIR, f), "utf-8");
    const masked = content.replace(/PrivateKey\s*=\s*.+/g, "PrivateKey = (hidden)");
    configs.push({ name: f, content: masked.trim() });
  }
  return configs;
}

async function readLogs() {
  const logFiles = [];
  if (!existsSync(LOG_DIR)) return logFiles;
  const files = await readdir(LOG_DIR);
  for (const f of files.filter((f) => f.endsWith(".err.log") || f.endsWith(".out.log")).sort()) {
    try {
      const content = await readFile(path.join(LOG_DIR, f), "utf-8");
      const lines = content.trim().split("\n").slice(-200);
      logFiles.push({ name: f, content: lines.join("\n"), size: Buffer.byteLength(content, "utf-8") });
    } catch {
      logFiles.push({ name: f, content: "", size: 0 });
    }
  }
  return logFiles;
}

function getUptime() {
  try {
    const started = parseInt(readFileSync(UPTIME_FILE, "utf-8").trim());
    const elapsed = Math.floor((Date.now() - started) / 1000);
    const h = Math.floor(elapsed / 3600);
    const m = Math.floor((elapsed % 3600) / 60);
    const s = elapsed % 60;
    return `服务已运行 ${h}h ${m}m ${s}s`;
  } catch {
    return "";
  }
}

// ---- API ----

app.get("/api/status", async (_req, res) => {
  try {
    const [raw, configs, logs] = await Promise.all([runWg(), readConfigs(), readLogs()]);
    const tunnels = parseWgOutput(raw);
    const uptime = getUptime();
    res.json({ tunnels, configs, logs, uptime });
  } catch (e) {
    res.status(500).json({ error: e.message });
  }
});

// ---- serve built frontend ----
const clientDist = path.join(__dirname, "client", "dist");
if (existsSync(clientDist)) {
  app.use(express.static(clientDist));
  app.get("*", (_req, res) => {
    res.sendFile(path.join(clientDist, "index.html"));
  });
}

// ---- start ----
app.listen(PORT, () => {
  writeFileSync(UPTIME_FILE, String(Date.now()));
  console.log(`WireGuard Dashboard → http://localhost:${PORT}`);
});
