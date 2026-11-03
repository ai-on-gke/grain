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

"""Stand-in for the GCE metadata server's identity endpoint, so a real
`grain hostd -iap-audience` (pkg/hostd/iap_auth.go, which honours
GCE_METADATA_HOST) gets an "ID token" the simulated IAP accepts
(tests/iap/iapsim.go: "iapsim-idtoken.<email>").

    metadata-stub.py PORT EMAIL      # GCE_METADATA_HOST=127.0.0.1:PORT grain hostd ...
"""
import http.server
import sys

PORT, EMAIL = int(sys.argv[1]), sys.argv[2]


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.headers.get("Metadata-Flavor") != "Google" or "/identity" not in self.path:
            self.send_response(404)
            self.end_headers()
            return
        body = ("iapsim-idtoken." + EMAIL).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


http.server.HTTPServer(("127.0.0.1", PORT), H).serve_forever()
