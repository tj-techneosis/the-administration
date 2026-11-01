#!/usr/bin/env python3
"""A tiny sign-up and notes app for trying the Administration on. It has a few
ordinary rough edges on purpose, so a first contract finds something.
Usage: server.py PORT"""
import email.parser, html, http.server, json, sys, urllib.parse

USERS = {"member@example.com": "pw-member", "staff@example.com": "pw-staff"}
NOTES = {}
FILES = {}  # who -> [(name, size)]
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
            files = "".join(f"<li>{html.escape(n)} ({size // 1024} KB)</li>" for n, size in FILES.get(who, []))
            self.send(200, page(f'<h1>Your notes</h1><ul>{items}</ul><form method="post" action="/notes">'
                                '<input name="text" placeholder="New note"><button class="save">Save</button></form>'
                                f'<h2>Attachments</h2><ul id="files">{files}</ul>'
                                '<form method="post" action="/attach" enctype="multipart/form-data">'
                                '<input type="file" name="file"> <button>Attach</button></form>'
                                '<div id="drop" style="margin-top:1rem;padding:2rem;border:2px dashed #999">Or drop files here</div>'
                                '<script>const d=document.getElementById("drop");'
                                'd.addEventListener("dragover",e=>e.preventDefault());'
                                'd.addEventListener("drop",async e=>{e.preventDefault();'
                                'for(const f of e.dataTransfer.files){const b=new FormData();b.append("file",f);'
                                'await fetch("/attach",{method:"POST",body:b});}location.reload();});</script>'))
        else:
            self.send(404, page("<h1>Not found</h1>"))

    def do_POST(self):
        if self.path == "/attach":
            return self.attach()
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

    def attach(self):
        who = self.user()
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n)
        msg = email.parser.BytesParser().parsebytes(
            b"Content-Type: " + self.headers.get("Content-Type", "").encode() + b"\r\n\r\n" + body)
        for part in msg.walk() if msg.is_multipart() else []:
            name = part.get_filename()
            if not name or not who:
                continue
            data = part.get_payload(decode=True) or b""
            if len(data) > 1 << 20:  # the limit nobody mentions
                return self.send(400, page("<p>Error: undefined</p>"))
            FILES.setdefault(who, []).append((name, len(data)))
        self.send(302, b"", headers=[("Location", "/notes")])

http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
