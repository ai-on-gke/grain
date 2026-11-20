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

"""A logging, delaying HTTP proxy in front of the GitHub simulator.

docs/test-plans/2026-11-14.md §3 item 4: every request is logged (one
line per upstream call, so listings can be counted) and the repository
listing and branch endpoints are held DELAY seconds first, standing in
for a slow git host. (socat has no delay option; this is the same
plumbing with one.)

    slowproxy.py LISTEN_PORT UPSTREAM_PORT LOG [DELAY]
"""
import http.client
import re
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN, UPSTREAM, LOG = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]
DELAY = float(sys.argv[4]) if len(sys.argv) > 4 else 3.0
SLOW = re.compile(r"^/(api/v3/)?(user/repos|installation/repositories|repos/[^/]+/[^/]+/branches)")
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def _forward(self):
        path = self.path
        slow = bool(SLOW.match(path))
        with lock, open(LOG, "a") as f:
            f.write(f"{time.time():.3f} {self.command} {path} slow={slow}\n")
        if slow:
            time.sleep(DELAY)
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n) if n else None
        conn = http.client.HTTPConnection("127.0.0.1", UPSTREAM, timeout=60)
        headers = {k: v for k, v in self.headers.items() if k.lower() not in ("host", "connection")}
        conn.request(self.command, path, body=body, headers=headers)
        resp = conn.getresponse()
        data = resp.read()
        self.send_response(resp.status)
        for k, v in resp.getheaders():
            if k.lower() not in ("transfer-encoding", "connection", "content-length"):
                self.send_header(k, v)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = do_HEAD = _forward


ThreadingHTTPServer(("127.0.0.1", LISTEN), Handler).serve_forever()
