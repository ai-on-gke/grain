#!/usr/bin/env python3
"""A stand-in api.github.com, for the half of a settings-reset hand run
that needs GitHub to say yes.

It answers exactly the two calls stateResetNotice.announce makes -- the
commits/{sha}/pulls lookup and the issues/{n}/comments POST -- and writes
every request it is sent to a log, bodies and all, so the comment grain
composed can be read verbatim afterwards. Everything else gets 404, which
is what an unexpected call should look like rather than a silent success.

Run it as root (it listens on 443, where the client expects
api.github.com) with a certificate for api.github.com; the daemon is
pointed at that certificate's issuer with SSL_CERT_FILE.
"""

import http.server
import json
import os
import ssl
import sys

LOG = os.environ.get("STANDIN_LOG", "/tmp/hr/standin.log")
PR_NUMBER = int(os.environ.get("STANDIN_PR", "1"))


def log(text):
    with open(LOG, "a") as f:
        f.write(text + "\n")


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def reply(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        log("GET %s" % self.path)
        if "/pulls" in self.path:
            self.reply(200, [{
                "number": PR_NUMBER,
                "html_url": "https://github.com/grain-handrun/state/pull/%d" % PR_NUMBER,
                "merged_at": "2026-09-08T00:00:00Z",
            }])
            return
        self.reply(404, {"message": "Not Found"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        log("POST %s (%d bytes)" % (self.path, len(raw)))
        if self.path.endswith("/comments"):
            body = json.loads(raw)["body"]
            name = LOG + ".comment"
            with open(name, "w") as f:
                f.write(body)
            log("comment body written to %s (%d bytes)" % (name, len(body)))
            self.reply(201, {"id": 424242})
            return
        self.reply(404, {"message": "Not Found"})

    def log_message(self, fmt, *args):
        pass


if __name__ == "__main__":
    certfile, keyfile = sys.argv[1], sys.argv[2]
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(certfile, keyfile)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 443), Handler)
    server.socket = ctx.wrap_socket(server.socket, server_side=True)
    log("stand-in listening on 127.0.0.1:443")
    server.serve_forever()
