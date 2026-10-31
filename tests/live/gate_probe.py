#!/usr/bin/env python3
"""Live check of the browser wall: a real Chromium through the pinned
Playwright MCP, configured the way a professional's is, against a room server
and an "other" local server standing in for everything that isn't the room.
Passes only if the other server never hears from the browser.
  gate_probe.py ADMINISTRATION_BIN TOOLS_DIR
Run it inside tests/isolated.sh's namespace or a throwaway account.
"""
import http.server, json, os, subprocess, sys, tempfile, threading

admin, tools = sys.argv[1], sys.argv[2]
hits = {"other": 0}

class Room(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if self.path.startswith("/go"):
            self.send_response(302); self.send_header("Location", f"http://127.0.0.1:{other.server_port}/"); self.end_headers(); return
        self.send_response(200); self.send_header("Content-Type", "text/html"); self.end_headers()
        self.wfile.write(b"<h1>room</h1>")

class Other(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        hits["other"] += 1
        self.send_response(200); self.end_headers(); self.wfile.write(b"OTHER")
    do_POST = do_CONNECT = do_GET

room = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Room)
other = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Other)
for s in (room, other):
    threading.Thread(target=s.serve_forever, daemon=True).start()
R, O = room.server_port, other.server_port

base = tempfile.mkdtemp(prefix="gate-")
desk, app, tmp = (os.path.join(base, d) for d in ("desk", "app", "tmp"))
for d in (desk, app, tmp):
    os.makedirs(d)
gl = os.path.join(base, "gate.log")
gate = subprocess.Popen([admin, "_gate", str(R)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
                        env={**os.environ, "ADMINISTRATION_GATE_LOG": gl})
gaddr = gate.stdout.readline().strip()
cfg = {"browser": {"browserName": "chromium", "isolated": True,
                   "launchOptions": {"headless": True, "chromiumSandbox": os.environ.get("PROBE_SANDBOX", "1") == "1",
                                     "proxy": {"server": "http://" + gaddr, "bypass": "<-loopback>"},
                                     "args": ["--proxy-bypass-list=<-loopback>", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
                                              "--webrtc-ip-handling-policy=disable_non_proxied_udp", "--dns-prefetch-disable"]},
                   "contextOptions": {"serviceWorkers": "block"}},
       "network": {"allowedOrigins": [f"http://127.0.0.1:{R}", f"http://localhost:{R}"]},
       "outputDir": os.path.join(desk, ".shots")}
cfgp = os.path.join(base, "browser.json")
json.dump(cfg, open(cfgp, "w"))
mcp = subprocess.Popen(["node", os.path.join(tools, "node_modules/@playwright/mcp/cli.js"), "--config", cfgp],
                       cwd=desk, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
                       env={**os.environ, "TMPDIR": tmp})
n = 0
def call(method, params=None, notify=False):
    global n
    msg = {"jsonrpc": "2.0", "method": method, "params": params or {}}
    if not notify:
        n += 1; msg["id"] = n
    mcp.stdin.write(json.dumps(msg) + "\n"); mcp.stdin.flush()
    if notify: return None
    while True:
        m = json.loads(mcp.stdout.readline())
        if m.get("id") == n: return m
def text(r): return " ".join(c.get("text", "") for c in r.get("result", {}).get("content", []))
def tool(name, **a): return text(call("tools/call", {"name": name, "arguments": a}))
def js(code): return tool("browser_evaluate", function=code)

call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "probe", "version": "0"}})
call("notifications/initialized", notify=True)
fails = []
def check(label, ok, detail=""):
    print(("PASS" if ok else "FAIL"), label, ("| " + " ".join(detail.split())[:140]) if detail else "")
    if not ok: fails.append(label)

t = tool("browser_navigate", url=f"http://127.0.0.1:{R}/")
check("room loads", f"Page URL: http://127.0.0.1:{R}/" in t and "### Error" not in t, t)
if fails:
    print("the browser never reached the room, so nothing below means anything"); sys.exit(2)
for u in (f"http://127.0.0.1:{O}/", f"http://localhost:{O}/", "https://example.com/", f"http://127.0.0.1:{R}/go"):
    tool("browser_navigate", url=u)
tool("browser_navigate", url=f"http://127.0.0.1:{R}/")
r = js(f"async () => {{ try {{ await fetch('http://127.0.0.1:{O}/', {{mode: 'no-cors'}}); return 'GOT' }} catch (e) {{ return 'NO' }} }}")
check("fetch to another port refused", '"NO"' in r, r)
for ws in (f"ws://127.0.0.1:{O}/", "wss://echo.websocket.org/"):
    r = js("() => new Promise(res => { const w = new WebSocket('%s'); w.onopen = () => res('OPEN'); w.onerror = () => res('ERR'); setTimeout(() => res('TIMEOUT'), 8000) })" % ws)
    check(f"websocket {ws} refused", '"OPEN"' not in r, r)
r = tool("browser_evaluate", function="() => 'pwned'", filename="../app/written.txt")
check("no writes outside the desk", not os.path.exists(os.path.join(app, "written.txt")), r)
r = tool("browser_navigate", url="file:///etc/hostname"); check("file: refused", "blocked" in r.lower() or "error" in r.lower(), r)
check("the other server heard nothing", hits["other"] == 0, f"{hits['other']} requests")
mcp.kill(); gate.stdin.close(); gate.wait()
print("gate log:", open(gl).read().strip().replace("\n", " | ")[:400])
sys.exit(1 if fails else 0)
