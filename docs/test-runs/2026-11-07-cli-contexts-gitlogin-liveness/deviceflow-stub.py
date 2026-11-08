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

"""A GitHub OAuth device-flow stub for `grain git-login -web` (§8 item 3).

POST /login/device/code         -> a device code, interval 1s
POST /login/oauth/access_token  -> the scripted answers in SCRIPT, in order
GET  /user, /api/v3/user        -> the signed-in user (for the author name)
GET  /_log                      -> every request seen, with the gap since the
                                   previous token poll (to check slow_down)

usage: deviceflow-stub.py <port> <answer,answer,...>
  answers: pending | slow_down | expired | denied | token
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])
SCRIPT = sys.argv[2].split(",")
LOG = []
STATE = {"i": 0, "last_poll": None}
TOKEN = "gho_walkthroughDeviceFlowToken0123456789"


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, body):
        raw = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        form = self.rfile.read(n).decode()
        if self.path == "/login/device/code":
            LOG.append({"path": self.path, "form": form})
            return self.send(200, {"device_code": "dev-123", "user_code": "WDJB-MJHT",
                                   "verification_uri": "http://127.0.0.1:%d/login/device" % PORT,
                                   "expires_in": 900, "interval": 1})
        if self.path == "/login/oauth/access_token":
            now = time.monotonic()
            gap = None if STATE["last_poll"] is None else round(now - STATE["last_poll"], 2)
            STATE["last_poll"] = now
            step = SCRIPT[min(STATE["i"], len(SCRIPT) - 1)]
            STATE["i"] += 1
            LOG.append({"path": self.path, "answer": step, "gap_s": gap,
                        "grant_type_ok": "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code" in form})
            if step == "pending":
                return self.send(200, {"error": "authorization_pending"})
            if step == "slow_down":
                return self.send(200, {"error": "slow_down", "interval": 3})
            if step == "expired":
                return self.send(200, {"error": "expired_token"})
            if step == "denied":
                return self.send(200, {"error": "access_denied"})
            return self.send(200, {"access_token": TOKEN, "token_type": "bearer", "scope": "repo,read:org,workflow"})
        self.send(404, {"message": "Not Found"})

    def do_GET(self):
        if self.path == "/_log":
            return self.send(200, LOG)
        LOG.append({"path": self.path, "auth": (self.headers.get("Authorization") or "")[:12] + "..."})
        if self.path in ("/user", "/api/v3/user"):
            return self.send(200, {"login": "octo-dev", "name": "Octo Dev", "id": 42})
        self.send(404, {"message": "Not Found"})


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
