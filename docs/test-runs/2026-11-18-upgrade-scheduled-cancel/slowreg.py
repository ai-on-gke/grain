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

"""slowreg.py: a TCP proxy from 127.0.0.1:5001 to the walk's registry:2 on
localhost:5000 that sends registry responses at RATE bytes/s (default
256 KiB/s), so an upgrade pulling localhost:5001/acme/grain:<tag> sits in
its pull step long enough for a cancel to land there. Docker must list
localhost:5001 in insecure-registries, as it does localhost:5000.

    python3 slowreg.py &   # RATE=262144 LISTEN=5001 UPSTREAM=5000
"""

import os
import socket
import threading
import time

RATE = int(os.environ.get("RATE", 256 * 1024))
LISTEN = int(os.environ.get("LISTEN", 5001))
UPSTREAM = int(os.environ.get("UPSTREAM", 5000))


def pump(src, dst, rate):
    try:
        while True:
            data = src.recv(16384)
            if not data:
                break
            dst.sendall(data)
            if rate:
                time.sleep(len(data) / rate)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def main():
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", LISTEN))
    srv.listen(64)
    while True:
        client, _ = srv.accept()
        up = socket.create_connection(("127.0.0.1", UPSTREAM))
        threading.Thread(target=pump, args=(client, up, 0), daemon=True).start()
        threading.Thread(target=pump, args=(up, client, RATE), daemon=True).start()


if __name__ == "__main__":
    main()
