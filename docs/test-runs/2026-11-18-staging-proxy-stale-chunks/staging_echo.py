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

# Hostile staging app for docs/test-plans/2026-11-18.md §7. Run as the
# persistent sandbox "preview" (acme/widgets) behind /staging/preview/:
#   GET  /echo...   -> JSON: raw request header lines in arrival order and
#                      case, every Cookie line, path
#   GET  /plant     -> sets hostile cookies (grain_*, __Host-*, IAP, Domain=)
#                      plus one legitimate cookie, and Clear-Site-Data
#   GET  /page      -> small HTML page (for browser walks)
#   GET  /ws        -> WebSocket upgrade: answers 101 (with a hostile
#                      Set-Cookie), sends one text frame holding the
#                      handshake's request headers, then closes
import base64
import hashlib
import http.server
import json
import os
import struct

PORT = int(os.environ.get("STAGING_PORT", "4281"))
WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

HOSTILE = [
    "grain_session=planted-session; Path=/",
    "grain_access=planted-access; Path=/; HttpOnly",
    "GRAIN_Verified=planted-upper; Path=/",
    "__Host-grain_verified=planted-host; Path=/; Secure; HttpOnly",
    "__host-x=planted-host-lower; Path=/; Secure",
    "__Host-x=planted-host-x; Path=/; Secure",
    "__Secure-grain_session=planted-secure; Path=/; Secure",
    "GCP_IAP_UID=planted-iap-uid; Path=/",
    "__Secure-GCP_IAP_AUTH_TOKEN_0123=planted-iap-token; Path=/; Secure",
    "GCP_IAAP_AUTH_TOKEN=planted-iaap; Path=/",
    "domain_cookie=planted-domain; Domain=127.0.0.1; Path=/",
    "rooted=planted-root-path; Path=/",
    'quoted_app="hello world"; Path=/staging/preview',
    "staging_app=ok; Path=/staging/preview; HttpOnly",
]


def raw_headers(msg):
    # http.client.HTTPMessage keeps every header line in arrival order with
    # its original case, duplicates included.
    return [[k, v] for k, v in msg.raw_items()]


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _json(self, obj, extra=()):
        body = (json.dumps(obj, indent=1) + "\n").encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        for k, v in extra:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.headers.get("Upgrade", "").lower() == "websocket":
            return self._websocket()
        if self.path.startswith("/plant"):
            extra = [("Set-Cookie", c) for c in HOSTILE]
            extra.append(("Clear-Site-Data", '"cookies", "storage"'))
            return self._json({"planted": len(HOSTILE)}, extra)
        if self.path.startswith("/page"):
            body = b"<!doctype html><title>staging preview</title><h1>staging preview</h1>\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Set-Cookie", "grain_session=planted-from-page; Path=/")
            self.end_headers()
            self.wfile.write(body)
            return
        self._json({
            "path": self.path,
            "cookieLines": self.headers.get_all("Cookie") or [],
            "headers": raw_headers(self.headers),
        })

    do_POST = do_GET

    def _websocket(self):
        key = self.headers.get("Sec-WebSocket-Key", "")
        accept = base64.b64encode(hashlib.sha1((key + WS_GUID).encode()).digest()).decode()
        self.send_response(101)
        self.send_header("Upgrade", "websocket")
        self.send_header("Connection", "Upgrade")
        self.send_header("Sec-WebSocket-Accept", accept)
        self.send_header("Set-Cookie", "grain_session=planted-ws; Path=/")
        self.send_header("Set-Cookie", "ws_app=ok; Path=/")
        self.end_headers()
        payload = json.dumps({
            "path": self.path,
            "cookieLines": self.headers.get_all("Cookie") or [],
            "headers": raw_headers(self.headers),
        }).encode()
        n = len(payload)
        if n < 126:
            hdr = struct.pack("!BB", 0x81, n)
        elif n < 65536:
            hdr = struct.pack("!BBH", 0x81, 126, n)
        else:
            hdr = struct.pack("!BBQ", 0x81, 127, n)
        self.wfile.write(hdr + payload + struct.pack("!BB", 0x88, 0))
        self.wfile.flush()
        self.close_connection = True


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
