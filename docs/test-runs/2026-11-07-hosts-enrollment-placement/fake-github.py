#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""A loopback stand-in for GitHub's OAuth device flow and /user, for the
2026-11-07 hosts walkthrough (docs/test-runs/2026-11-07-hosts-enrollment-placement.md).

A daemon with no identity proxy answers POST /api/hostd/join with a
github_device challenge; pointing its github_host at this server (with
github_insecure_http) lets `grain host -join` and `grain-agentd -join`
complete the device flow without reaching github.com. Every device code is
authorized at once. The login a token maps to is chosen by the user_code
prefix the client never sees, so instead it is picked from $FAKE_GH_LOGIN
at the moment the token is minted (rewrite the file named by
$FAKE_GH_LOGIN_FILE to switch identities between joins).

usage: fake-github.py <port>
"""
import json
import os
import sys
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 4291
LOGIN_FILE = os.environ.get("FAKE_GH_LOGIN_FILE", "/tmp/fake-gh-login")
TOKENS = {}
N = [0]


def current_login():
    try:
        with open(LOGIN_FILE) as f:
            v = f.read().strip()
            if v:
                return v
    except OSError:
        pass
    return os.environ.get("FAKE_GH_LOGIN", "walkthrough-owner")


class H(BaseHTTPRequestHandler):
    def _send(self, code, body):
        raw = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        form = urllib.parse.parse_qs(self.rfile.read(n).decode())
        path = urllib.parse.urlparse(self.path).path
        if path == "/login/device/code":
            N[0] += 1
            self._send(200, {
                "device_code": "dc-%d" % N[0],
                "user_code": "WALK-%04d" % N[0],
                "verification_uri": "http://127.0.0.1:%d/login/device" % PORT,
                "expires_in": 900,
                "interval": 1,
            })
            return
        if path == "/login/oauth/access_token":
            dc = (form.get("device_code") or [""])[0]
            tok = "gho_fake_%s" % dc
            TOKENS[tok] = current_login()
            self._send(200, {"access_token": tok, "token_type": "bearer"})
            return
        self._send(404, {"message": "Not Found"})

    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        auth = self.headers.get("Authorization", "")
        tok = auth.replace("Bearer ", "").replace("token ", "").strip()
        login = TOKENS.get(tok)
        if path in ("/user", "/api/v3/user"):
            if not login:
                self._send(401, {"message": "Bad credentials"})
                return
            self._send(200, {"login": login, "id": 1000 + len(login)})
            return
        if path in ("/user/orgs", "/api/v3/user/orgs"):
            self._send(200, [{"login": "walkthrough-org"}] if login else [])
            return
        self._send(404, {"message": "Not Found"})

    def log_message(self, fmt, *args):
        sys.stderr.write("fake-github: " + (fmt % args) + "\n")


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
