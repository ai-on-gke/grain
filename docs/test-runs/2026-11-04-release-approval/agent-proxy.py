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

"""agent-proxy.py LISTEN_PORT UPSTREAM TASK_ID

The one stubbed boundary in §2 items 1 and 3: the sandbox proxy a run's
`grain` CLI talks through (127.0.0.1:9728 inside a sandbox), which marks
every request it forwards as that run's task acting on the agent API
(X-Grain-Acting-Task, X-Grain-Channel: sandbox-cli; pkg/ui/agent_api_gate.go).
The demo dispatches no runs, so this forwards the CLI's requests to the
demo with the same two headers, making each call the agent's.
"""
import http.server
import sys
import urllib.error
import urllib.request

PORT, UPSTREAM, TASK = int(sys.argv[1]), sys.argv[2].rstrip("/"), sys.argv[3]


class Proxy(http.server.BaseHTTPRequestHandler):
    def _forward(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else None
        req = urllib.request.Request(UPSTREAM + self.path, data=body, method=self.command)
        for k, v in self.headers.items():
            if k.lower() not in ("host", "content-length", "connection"):
                req.add_header(k, v)
        req.add_header("X-Grain-Acting-Task", TASK)
        req.add_header("X-Grain-Channel", "sandbox-cli")
        try:
            resp = urllib.request.urlopen(req)
            status, headers, data = resp.status, resp.headers, resp.read()
        except urllib.error.HTTPError as e:
            status, headers, data = e.code, e.headers, e.read()
        self.send_response(status)
        for k, v in headers.items():
            if k.lower() not in ("transfer-encoding", "connection", "content-length"):
                self.send_header(k, v)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = _forward

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Proxy).serve_forever()
