#!/usr/bin/env python3
"""A tiny sign-up and notes app for trying the Administration on. It has a few
ordinary rough edges on purpose, so a first contract finds something.
Usage: server.py PORT"""
import html, http.server, json, sys, urllib.parse

USERS = {"member@example.com": "pw-member", "staff@example.com": "pw-staff"}
NOTES = {}
PAGE = """<!doctype html><html><head><meta charset="utf-8"><title>Notes</title>
<style>body{{font:16px system-ui;margin:2rem;width:900px}} .row{{display:flex;gap:1rem}}
input{{width:420px}} button.save{{position:absolute;left:760px}}</style></head><body>{body}</body></html>"""

def page(body):
    return PAGE.format(body=body).encode()

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, body, ctype="text/html", headers=()):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def user(self):
        for part in self.headers.get("Cookie", "").split(";"):
            k, _, v = part.strip().partition("=")
            if k == "who" and v in USERS:
                return v
        return None

    def form(self):
        n = int(self.headers.get("Content-Length") or 0)
        return {k: v[0] for k, v in urllib.parse.parse_qs(self.rfile.read(n).decode()).items()}

    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        if u.path == "/":
            self.send(200, page('<h1>Notes</h1><p>Keep your notes in one place.</p>'
                                '<p><a href="/signup">Get started</a> · <a href="/login">Sign in</a></p>'))
        elif u.path == "/signup":
            self.send(200, page('<h1>Create your account</h1><form method="post" action="/signup">'
                                '<div class="row"><input name="email" placeholder="Email"></div>'
                                '<div class="row"><input name="pw" type="password" placeholder="Password (8+)"></div>'
                                '<button>Continue</button></form>'))
        elif u.path == "/login":
            self.send(200, page('<h1>Sign in</h1><form method="post" action="/login">'
                                '<input name="email" placeholder="Email"><input name="pw" type="password">'
                                '<button>Sign in</button></form>'))
        elif u.path == "/notes":
            who = self.user()
            if not who:
                return self.send(302, b"", headers=[("Location", "/login")])
            items = "".join(f"<li>{html.escape(n)}</li>" for n in NOTES.get(who, []))
            self.send(200, page(f'<h1>Your notes</h1><ul>{items}</ul><form method="post" action="/notes">'
                                '<input name="text" placeholder="New note"><button class="save">Save</button></form>'))
        else:
            self.send(404, page("<h1>Not found</h1>"))

    def do_POST(self):
        f = self.form()
        if self.path == "/signup":
            if "@" not in f.get("email", ""):
                return self.send(400, page("<p>Error: undefined</p>"))
            if len(f.get("pw", "")) < 8:
                return self.send(400, page("<p>Invalid input.</p>"))
            USERS[f["email"]] = f["pw"]
            return self.send(302, b"", headers=[("Location", "/notes"), ("Set-Cookie", f"who={f['email']}; Path=/")])
        if self.path == "/login":
            if USERS.get(f.get("email")) == f.get("pw"):
                return self.send(302, b"", headers=[("Location", "/notes"), ("Set-Cookie", f"who={f['email']}; Path=/")])
            return self.send(401, page("<p>Wrong email or password.</p>"))
        if self.path == "/notes":
            who = self.user()
            if who and f.get("text"):
                NOTES.setdefault(who, []).append(f["text"])
            return self.send(302, b"", headers=[("Location", "/notes")])
        self.send(404, b"")

http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
