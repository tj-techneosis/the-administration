#!/usr/bin/env python3
"""Drive the Playwright MCP server the way a professional's claude would, from
an mcp.json the Administration wrote, and report what loads. Usage:
  mcp_probe.py MCP_JSON CWD URL...
Each URL prints LOADED or BLOCKED with the first line of the tool's answer.
"""
import json, subprocess, sys

cfg = json.load(open(sys.argv[1]))["mcpServers"]["playwright"]
cwd, urls = sys.argv[2], sys.argv[3:]
p = subprocess.Popen([cfg["command"], *cfg["args"]], cwd=cwd, stdin=subprocess.PIPE,
                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
n = 0
def call(method, params=None, notify=False):
    global n
    msg = {"jsonrpc": "2.0", "method": method, "params": params or {}}
    if not notify:
        n += 1
        msg["id"] = n
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
    if notify:
        return None
    while True:
        m = json.loads(p.stdout.readline())
        if m.get("id") == n:
            return m
call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "probe", "version": "0"}})
call("notifications/initialized", notify=True)
for u in urls:
    if u.startswith("fetch:"):
        # From inside the page: can script reach what navigation can't?
        target = u[6:]
        r = call("tools/call", {"name": "browser_evaluate", "arguments": {"function":
            "async () => { try { const r = await fetch(%s, {mode: 'no-cors'}); return 'GOT ' + r.type + ' ' + r.status; } catch (e) { return 'BLOCKED-FETCH ' + e; } }" % json.dumps(target)}})
        text = " ".join(c.get("text", "") for c in r.get("result", {}).get("content", []))
        print(("LOADED" if "\"GOT " in text else "BLOCKED"), u, "|", " ".join(text.split())[:160])
        continue
    r = call("tools/call", {"name": "browser_navigate", "arguments": {"url": u}})
    text = " ".join(c.get("text", "") for c in r.get("result", {}).get("content", []))
    err = r.get("result", {}).get("isError") or "error" in r
    blocked = err or "ERR_BLOCKED" in text or "blocked" in text.lower() or "not allowed" in text.lower()
    print(("BLOCKED" if blocked else "LOADED"), u, "|", " ".join(text.split())[:160])
p.kill()
