// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package portforward

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// GuestForwarder is the sandbox half of a forward, run by python3 in the
// guest as the sandbox user:
//
//	python3 forwarder.py <sandbox_port> <relay host> <relay port> <token file>
//
// It binds 127.0.0.1:<sandbox_port> first and only then detaches, so its
// exit status says whether the port is up: 0 once it is listening, non-zero
// (with the reason on stderr) when it could not bind. Each accepted
// connection is carried to the relay by an HTTP Upgrade presenting the
// token read from <token file>, the header read a byte at a time so no
// byte of the stream itself is consumed with it.
const GuestForwarder = `import os, socket, sys, threading
port, host, rport, tokfile = int(sys.argv[1]), sys.argv[2], int(sys.argv[3]), sys.argv[4]
with open(tokfile) as f:
    token = f.read().strip()
ls = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
try:
    ls.bind(("127.0.0.1", port))
    ls.listen(64)
except OSError as e:
    sys.stderr.write("cannot listen on 127.0.0.1:%d: %s\n" % (port, e))
    sys.exit(1)
if os.fork():
    os._exit(0)
os.setsid()
null = os.open(os.devnull, os.O_RDWR)
for fd in (0, 1, 2):
    os.dup2(null, fd)
def pipe(a, b):
    try:
        while True:
            d = a.recv(65536)
            if not d:
                break
            b.sendall(d)
    except OSError:
        pass
    try:
        b.shutdown(socket.SHUT_WR)
    except OSError:
        pass
def serve(c):
    try:
        u = socket.create_connection((host, rport), timeout=30)
        u.sendall(("GET ` + Path + ` HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n"
                   "Connection: Upgrade\r\nUpgrade: ` + UpgradeProtocol + `\r\n\r\n" % (host, token)).encode())
        head = b""
        while not head.endswith(b"\r\n\r\n"):
            d = u.recv(1)
            if not d or len(head) > 8192:
                raise OSError("relay refused the forward")
            head += d
        if not head.startswith(b"HTTP/1.1 101"):
            raise OSError("relay refused the forward")
        u.settimeout(None)
    except OSError:
        c.close()
        return
    t = threading.Thread(target=pipe, args=(u, c), daemon=True)
    t.start()
    pipe(c, u)
    t.join()
    c.close()
    u.close()
while True:
    c, _ = ls.accept()
    threading.Thread(target=serve, args=(c,), daemon=True).start()
`

// RelayAddress splits the URL the relay is published at into the host
// and port a guest forwarder dials.
func RelayAddress(relayURL string) (host string, port int, err error) {
	u, err := url.Parse(relayURL)
	if err != nil || u.Host == "" {
		return "", 0, fmt.Errorf("capability port relay URL %q is not usable", relayURL)
	}
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", 0, fmt.Errorf("capability port relay URL %q names no port", relayURL)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, fmt.Errorf("capability port relay URL %q names no port", relayURL)
	}
	return h, n, nil
}
