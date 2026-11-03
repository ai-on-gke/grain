#!/usr/bin/env python3
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
