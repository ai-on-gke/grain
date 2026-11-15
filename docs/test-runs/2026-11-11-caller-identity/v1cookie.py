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

"""Mint a grain_access session cookie the way pkg/ui/accesscode.go does.

    v1cookie.py CODE [NAME] [ISSUED_UNIX]

With no NAME it is a v1 cookie ("v1.<secs>.<mac>"), the shape a browser
signed in before named sessions still holds; with NAME it is v2.
"""

import base64
import hashlib
import hmac
import sys
import time


def canonical(code):
    out = []
    for ch in code:
        if ch in "- \t\n\r":
            continue
        out.append(ch.upper() if "a" <= ch <= "z" else ch)
    return "".join(out)


def b64(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def main():
    code = sys.argv[1]
    name = sys.argv[2] if len(sys.argv) > 2 else ""
    issued = int(sys.argv[3]) if len(sys.argv) > 3 else int(time.time())
    key = hashlib.sha256(("grain access session\x00" + canonical(code)).encode()).digest()
    payload = "v1.%d" % issued
    if name:
        payload = "v2.%d.%s" % (issued, b64(name.encode()))
    mac = hmac.new(key, payload.encode(), hashlib.sha256).digest()
    print(payload + "." + b64(mac))


if __name__ == "__main__":
    main()
