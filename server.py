#!/usr/bin/env python3
"""wg-service — 轻量 HTTP 服务（带密码鉴权）"""
import json
import os
import re
import secrets
import hashlib
import subprocess
import time
from http.server import HTTPServer, SimpleHTTPRequestHandler
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent

# ---- 配置：环境变量 > .env 文件 > 默认值 ----
def _load_env():
    env = {}
    env_file = SCRIPT_DIR / ".env"
    if env_file.is_file():
        for line in env_file.read_text().split("\n"):
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                k, v = line.split("=", 1)
                env[k.strip()] = v.strip()
    return env

_env = _load_env()

PORT = int(os.environ.get("UI_PORT", _env.get("UI_PORT", "4623")))
PASSWORD = os.environ.get("UI_PASSWORD", _env.get("UI_PASSWORD", "admin"))
WG_BIN = os.environ.get("WG_BIN", _env.get("WG_BIN", "/opt/homebrew/opt/wireguard-tools/bin/wg"))
CONFIG_DIR = Path(os.environ.get("CONFIG_DIR", _env.get("CONFIG_DIR", "/usr/local/etc/wireguard")))
LOG_DIR = Path(os.environ.get("LOG_DIR", _env.get("LOG_DIR", "/var/log/wireguard")))

START_TIME = time.time()

_SESSION_TOKEN = secrets.token_hex(32)
_TOKEN_HASH = hashlib.sha256(f"sid:{_SESSION_TOKEN}".encode()).hexdigest()

# ---- 数据获取 ----

def run_wg():
    try:
        cmd = [WG_BIN, "show", "all"] if os.geteuid() == 0 else ["sudo", WG_BIN, "show", "all"]
        return subprocess.run(cmd, capture_output=True, text=True, timeout=5).stdout
    except Exception:
        return ""


def parse_wg(raw):
    tunnels, current, current_peer = [], None, None
    for line in raw.split("\n"):
        if m := re.match(r"^interface:\s+(.+)", line):
            if current:
                tunnels.append(current)
            current = {"interface": m[1], "publicKey": "", "listeningPort": "", "peers": []}
            current_peer = None
            continue
        if not current:
            continue
        if m := re.match(r"^\s+public key:\s+(.+)", line):
            current["publicKey"] = m[1]
        elif m := re.match(r"^\s+listening port:\s+(.+)", line):
            current["listeningPort"] = m[1]
        elif m := re.match(r"^peer:\s+(.+)", line):
            current_peer = {
                "publicKey": m[1], "endpoint": "", "allowedIps": "",
                "handshake": "", "transferRx": "", "transferTx": "",
            }
            current["peers"].append(current_peer)
        elif not current_peer:
            continue
        elif m := re.match(r"^\s+endpoint:\s+(.+)", line):
            current_peer["endpoint"] = m[1]
        elif m := re.match(r"^\s+allowed ips:\s+(.+)", line):
            current_peer["allowedIps"] = m[1]
        elif m := re.match(r"^\s+latest handshake:\s+(.+)", line):
            current_peer["handshake"] = m[1]
        elif m := re.match(r"^\s+transfer:\s+(.+)\s+received,\s+(.+)\s+sent", line):
            current_peer["transferRx"] = m[1]
            current_peer["transferTx"] = m[2]
    if current:
        tunnels.append(current)
    return tunnels


def read_configs():
    configs = []
    if not CONFIG_DIR.is_dir():
        return configs
    for f in sorted(CONFIG_DIR.glob("*.conf")):
        content = f.read_text()
        content = re.sub(r"PrivateKey\s*=\s*.+", "PrivateKey = (hidden)", content)
        configs.append({"name": f.name, "content": content.strip()})
    return configs


def _tail(path, n=500):
    """用 subprocess tail 只读最后 N 行，避免大文件全量加载到内存"""
    try:
        r = subprocess.run(
            ["tail", "-n", str(n), str(path)],
            capture_output=True, text=True, timeout=5,
        )
        return r.stdout.strip().split("\n") if r.stdout.strip() else []
    except Exception:
        return []


def read_logs():
    """合并同名服务的 err/out 日志为一个条目"""
    merged = {}
    if not LOG_DIR.is_dir():
        return []

    for f in sorted(LOG_DIR.iterdir()):
        if not (f.name.endswith(".err.log") or f.name.endswith(".out.log")):
            continue
        service = f.name.replace(".err.log", "").replace(".out.log", "")
        label = "err" if ".err" in f.name else "out"
        if service not in merged:
            merged[service] = {"name": service, "lines": [], "size": 0}
        try:
            lines = _tail(f, n=500)
            for line in lines:
                merged[service]["lines"].append(f"[{label}] {line}")
            merged[service]["size"] += f.stat().st_size
        except Exception:
            pass

    logs = []
    for service in sorted(merged.keys()):
        m = merged[service]
        logs.append({
            "name": service,
            "content": "\n".join(reversed(m["lines"])),
            "size": m["size"],
        })
    return logs


def get_uptime():
    elapsed = int(time.time() - START_TIME)
    h, r = divmod(elapsed, 3600)
    m, s = divmod(r, 60)
    return f"服务已运行 {h}h {m}m {s}s"


LOGIN_HTML = """<!DOCTYPE html><html lang="zh"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>WireGuard</title><style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:hsl(240 4.8% 95.9%);display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#fff;border:1px solid hsl(240 5.9% 90%);border-radius:8px;padding:32px;width:320px;box-shadow:0 1px 2px rgba(0,0,0,.04)}
h1{font-size:18px;font-weight:600;color:#111;margin-bottom:4px}
p{font-size:13px;color:#999;margin-bottom:20px}
input{width:100%;padding:8px 12px;border:1px solid hsl(240 5.9% 90%);border-radius:6px;font-size:14px;outline:none;margin-bottom:12px}
input:focus{border-color:#3b82f6;box-shadow:0 0 0 2px rgba(59,130,246,.15)}
button{width:100%;padding:8px;background:#111;color:#fff;border:none;border-radius:6px;font-size:14px;font-weight:500;cursor:pointer}
button:hover{background:#333}
.error{color:#ef4444;font-size:12px;margin-bottom:12px;display:none}
</style></head><body>
<div class="card"><h1>WireGuard</h1><p>请输入密码</p>
<form onsubmit="return login(event)"><input type="password" id="pw" placeholder="Password" autofocus><div class="error" id="err">密码错误</div><button type="submit">登录</button></form></div>
<script>
async function login(e){e.preventDefault();const r=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:document.getElementById('pw').value})});if(r.ok){location.replace('/')}else{document.getElementById('err').style.display='block'}}
</script></body></html>"""


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        dist = str(SCRIPT_DIR / "client" / "dist")
        super().__init__(*args, directory=dist)

    def _authed(self):
        cookie = self.headers.get("Cookie", "")
        return f"wg_token={_TOKEN_HASH}" in cookie

    def _set_auth_cookie(self):
        self.send_header(
            "Set-Cookie",
            f"wg_token={_TOKEN_HASH}; Path=/; HttpOnly; SameSite=Strict; Max-Age=86400",
        )

    def do_GET(self):
        if self.path == "/api/status":
            if not self._authed():
                self.send_response(401)
                self.end_headers()
                return
            raw = run_wg()
            self._send_json({
                "tunnels": parse_wg(raw),
                "configs": read_configs(),
                "logs": read_logs(),
                "uptime": get_uptime(),
            })
        elif self.path == "/login.html" or (self.path == "/" and not self._authed()):
            body = LOGIN_HTML.encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif self.path == "/" or self.path.startswith("/assets/"):
            if self._authed():
                super().do_GET()
            else:
                self.send_response(302)
                self.send_header("Location", "/login.html")
                self.end_headers()
        else:
            super().do_GET()

    def do_POST(self):
        if self.path == "/api/logs/clear":
            if not self._authed():
                self.send_response(401)
                self.end_headers()
                return
            count = 0
            if LOG_DIR.is_dir():
                for f in LOG_DIR.iterdir():
                    if f.name.endswith(".log"):
                        f.write_text("")
                        count += 1
            self._send_json({"ok": True, "cleared": count})
        elif self.path == "/api/login":
            content_length = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(content_length))
            if body.get("password") == PASSWORD:
                self.send_response(200)
                self._set_auth_cookie()
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"ok":true}')
            else:
                self.send_response(401)
                self.end_headers()
        else:
            self.send_response(404)
            self.end_headers()

    def _send_json(self, data):
        body = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass  # 禁用 access log


if __name__ == "__main__":
    server = HTTPServer(("127.0.0.1", PORT), Handler)
    print(f"wg-service → http://localhost:{PORT}")
    server.serve_forever()
