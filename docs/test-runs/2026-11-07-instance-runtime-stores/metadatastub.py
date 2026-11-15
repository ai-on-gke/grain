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

"""GCE metadata server stand-in for the 2026-11-07 §2 item 5 walkthrough.

Serves /computeMetadata/v1/instance/attributes/grain-config from the file
named by --config (404 when the file is missing), plus the default service
account's token and email. Requires the Metadata-Flavor: Google header the
way the real server does.

    python3 metadatastub.py --addr 127.0.0.1:4305 --config /tmp/walk/i5-grain-config.json
"""
import argparse
import http.server
import json
import os

ARGS = None


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.headers.get("Metadata-Flavor") != "Google":
            self.send_error(403, "Missing Metadata-Flavor: Google")
            return
        base = "/computeMetadata/v1/"
        path = self.path.split("?")[0]
        body = None
        if path == base + "instance/attributes/grain-config":
            if os.path.exists(ARGS.config):
                with open(ARGS.config, encoding="utf-8") as f:
                    body = f.read()
        elif path == base + "instance/service-accounts/default/token":
            body = json.dumps({"access_token": ARGS.token, "expires_in": 3599, "token_type": "Bearer"})
        elif path == base + "instance/service-accounts/default/email":
            body = "grain-inst-walk@walk-proj.iam.gserviceaccount.com"
        if body is None:
            self.send_error(404)
            return
        data = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):
        print("metadatastub: " + fmt % args, flush=True)


def main():
    global ARGS
    p = argparse.ArgumentParser()
    p.add_argument("--addr", default="127.0.0.1:4305")
    p.add_argument("--config", required=True)
    p.add_argument("--token", default="fake-sm-token")
    ARGS = p.parse_args()
    host, port = ARGS.addr.rsplit(":", 1)
    http.server.ThreadingHTTPServer((host, int(port)), Handler).serve_forever()


if __name__ == "__main__":
    main()
