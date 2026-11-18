#!/usr/bin/env bash
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

# Items 1 and 2 of docs/test-plans/2026-11-18.md §7: send every credential
# grain or its edge would carry, in every encoding curl can produce, through
# https://127.0.0.1:4292/staging/preview/ and check what staging_echo.py saw.
# Writes one redacted capture per case to $OUT and prints PASS/FAIL lines.
# Needs: curl, jq, python3 (for the raw-socket cases).
set -uo pipefail

P=${PROXY_HTTPS:-https://127.0.0.1:4292}
OUT=${OUT:-/tmp/walk-stale-captures}
mkdir -p "$OUT"
FAILS=0

# Names that must never reach the app (matched case-insensitively).
FORBIDDEN_HEADERS='^(authorization|proxy-authorization|x-grain-(access-code|access-name|tunnel-token|hub-token|registry-token|subject-token|on-behalf-of|acting-task|acting-run|acting-reason|channel|user|hub-assertion|registry-assertion)|grain-user|x-user-ldap|x-forwarded-user|x-slow-proxy-assertion|x-goog-iap-.*|x-goog-authenticated-user-.*)$'
FORBIDDEN_COOKIES='^((__secure-)?(grain_|gcp_iap|gcp_iaap)|__host-)'

# redact replaces every value we planted (all carry "secret-") and the
# harness's real signed assertion with a fixed marker.
redact() {
	sed -E 's/(secret-)[A-Za-z0-9._-]*/\1REDACTED/g; s/v1\|[^"|]*\|[^"|]*\|[0-9]+\|[0-9a-f]{64}/v1|REDACTED/g; s/(__Host-grain_verified=)v1\.[A-Za-z0-9._-]*/\1REDACTED/g'
}

check() { # name file
	local name=$1 f=$2 bad
	bad=$(jq -r --arg h "$FORBIDDEN_HEADERS" --arg c "$FORBIDDEN_COOKIES" '
		[(.headers[] | .[0] | ascii_downcase | gsub("_";"-") | select(test($h))),
		 (.cookieLines[] | split(";")[] | gsub("^\\s+|\\s+$";"") | split("=")[0] | select(ascii_downcase | test($c)))] | join(",")' "$f" 2>/dev/null)
	if [[ $? -ne 0 ]]; then
		echo "FAIL $name: no echo JSON ($(head -c 200 "$f"))"
		FAILS=$((FAILS + 1))
	elif [[ -n $bad ]]; then
		echo "FAIL $name: app saw $bad"
		FAILS=$((FAILS + 1))
	else
		echo "PASS $name: cookies=$(jq -c '.cookieLines' "$f" | redact)"
	fi
}

COOKIES='grain_session=secret-s1; grain_access=secret-a1; __Host-grain_verified=secret-v1; __Secure-grain_session=secret-s2; GCP_IAP_UID=secret-u1; __Secure-GCP_IAP_AUTH_TOKEN_0123=secret-t1; GCP_IAAP_AUTH_TOKEN=secret-t2; __Host-x=secret-h1; app_pref=dark'
CRED_HEADERS=(
	-H "Authorization: Bearer secret-bearer"
	-H "Proxy-Authorization: Basic secret-proxy"
	-H "X-Grain-Access-Code: secret-code"
	-H "X-Grain-Access-Name: alice"
	-H "X-Grain-Tunnel-Token: secret-tunnel"
	-H "X-Grain-Hub-Token: secret-hubtok"
	-H "X-Goog-IAP-JWT-Assertion: secret-iapjwt"
	-H "X-Grain-Hub-Assertion: secret-hubjwt"
	-H "X-Grain-Registry-Assertion: secret-regjwt"
	-H "X-Goog-Authenticated-User-Email: accounts.example.com:alice@example.com"
	-H "X-Goog-Authenticated-User-Id: accounts.example.com:secret-uid"
	-H "X-Goog-IAP-Generated-Custom: secret-custom"
	-H "X-Forwarded-User: alice"
	-H "X-User-LDAP: alice"
	-H "Grain-User: alice"
	-H "X-Grain-User: alice"
)

run() { # name curl-args...
	local name=$1
	shift
	curl -sk "$@" -o "$OUT/$name.raw" -w '%{http_version}' >"$OUT/$name.ver" 2>/dev/null
	redact <"$OUT/$name.raw" >"$OUT/$name.json"
	rm -f "$OUT/$name.raw"
	check "$name (HTTP/$(cat "$OUT/$name.ver"))" "$OUT/$name.json"
}

# 1. canonical names, one Cookie line, HTTP/1.1 and HTTP/2 to the edge.
run h1-canonical --http1.1 "${CRED_HEADERS[@]}" -H "Cookie: $COOKIES" "$P/staging/preview/echo"
run h2-canonical --http2 "${CRED_HEADERS[@]}" -H "Cookie: $COOKIES" "$P/staging/preview/echo"

# 1a. an agent-API header either makes grain answer the request itself
# (403: the app never sees it) or is stripped on the way through.
for h in "X-Grain-Acting-Task: 42" "X-Grain-Acting-Run: 7" "X-Grain-Channel: sandbox-cli"; do
	n=agent-${h%%:*}
	code=$(curl -sk -o "$OUT/$n.json" -w '%{http_code}' -H "$h" "$P/staging/preview/echo")
	if [[ $code == 403 ]]; then echo "PASS $n: refused with 403 by grain"; else check "$n ($code)" "$OUT/$n.json"; fi
done

# 2a. mixed and lower case names.
run mixed-case --http1.1 \
	-H "aUtHoRiZaTiOn: Bearer secret-mixed" -H "x-goog-iap-jwt-assertion: secret-lower" \
	-H "X-GOOG-IAP-JWT-ASSERTION: secret-upper2" -H "x-SLOW-proxy-ASSERTION: secret-slow" \
	-H "x-grain-access-code: secret-code2" -H "X-GRAIN-TUNNEL-TOKEN: secret-tun2" \
	-H "x-goog-authenticated-user-email: x:secret-mail" \
	-H "cookie: GRAIN_SESSION=secret-up; Gcp_Iap_Uid=secret-mix; __HOST-y=secret-hu; __secure-GRAIN_x=secret-sx; app_pref=dark" \
	"$P/staging/preview/echo"
run h2-mixed-case --http2 \
	-H "aUtHoRiZaTiOn: Bearer secret-mixed" -H "X-Goog-Iap-Jwt-Assertion: secret-lower" \
	-H "cookie: GRAIN_SESSION=secret-up; Gcp_Iap_Uid=secret-mix; app_pref=dark" \
	"$P/staging/preview/echo"

# 2b. duplicated headers and a Cookie header split over several lines.
run duplicated --http1.1 \
	-H "Authorization: Bearer secret-d1" -H "Authorization: Bearer secret-d2" \
	-H "X-Goog-IAP-JWT-Assertion: secret-d3" -H "X-Goog-IAP-JWT-Assertion: secret-d4" \
	-H "Cookie: app_a=1; grain_session=secret-c1" -H "Cookie: grain_access=secret-c2" \
	-H "Cookie: __Host-grain_verified=secret-c3; app_b=2" -H "Cookie: GCP_IAP_UID=secret-c4" \
	"$P/staging/preview/echo"
run h2-split-cookie --http2 \
	-H "Cookie: app_a=1; grain_session=secret-c1" -H "Cookie: grain_access=secret-c2" \
	-H "Cookie: __Host-grain_verified=secret-c3; app_b=2" "$P/staging/preview/echo"

# 2c. whitespace and quoted values, empty segments, a nameless pair.
run quoted --http1.1 \
	-H 'Cookie:   grain_session = "secret-q1" ;app_q="quoted value";;  GCP_IAP_UID="secret-q2"  ; app_ws=  padded  ; =secret-nameless; __Host-grain_verified="secret-q3"' \
	"$P/staging/preview/echo"

# 2d. prefix not stripped by a path trick (encoded slash, dot segments).
run dot-segments --http1.1 -L --path-as-is "${CRED_HEADERS[@]}" -H "Cookie: $COOKIES" "$P/staging/preview/./a/../echo"
run encoded-path --http1.1 "${CRED_HEADERS[@]}" -H "Cookie: $COOKIES" "$P/staging/preview/%2e%2e/echo"

# 2e. raw HTTP/1.1 through TLS with obs-fold continuation and odd spacing
# (curl normalises these, so send bytes ourselves).
python3 - "$OUT" <<'PY'
import socket, ssl, sys, json, re
out = sys.argv[1]
def send(name, raw):
    ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
    s = ctx.wrap_socket(socket.create_connection(("127.0.0.1", 4292)), server_hostname="localhost")
    s.sendall(raw)
    data = b""
    while True:
        b = s.recv(65536)
        if not b: break
        data += b
    head, _, body = data.partition(b"\r\n\r\n")
    status = head.split(b"\r\n")[0].decode()
    body = re.sub(rb"secret-[A-Za-z0-9._-]*", b"secret-REDACTED", body)
    open(f"{out}/{name}.json", "wb").write(body if body.strip().startswith(b"{") else json.dumps({"status": status, "body": body.decode(errors="replace")[:300], "headers": [], "cookieLines": []}).encode())
    open(f"{out}/{name}.ver", "w").write("1.1 " + status)
send("raw-obs-fold",
     b"GET /staging/preview/echo HTTP/1.1\r\nHost: 127.0.0.1:4292\r\n"
     b"Authorization: Bearer\r\n secret-folded\r\n"
     b"Cookie: app_a=1;\r\n grain_session=secret-fold\r\nConnection: close\r\n\r\n")
send("raw-space-before-colon",
     b"GET /staging/preview/echo HTTP/1.1\r\nHost: 127.0.0.1:4292\r\n"
     b"Authorization : Bearer secret-sp\r\nConnection: close\r\n\r\n")
send("raw-underscore-names",
     b"GET /staging/preview/echo HTTP/1.1\r\nHost: 127.0.0.1:4292\r\n"
     b"X_Goog_IAP_JWT_Assertion: secret-underscore\r\nx_grain_access_code: secret-us2\r\nConnection: close\r\n\r\n")
PY
check "raw-underscore-names" "$OUT/raw-underscore-names.json"
for n in raw-obs-fold raw-space-before-colon raw-underscore-names; do
	echo "INFO $n: $(cat "$OUT/$n.ver"): $(jq -c '[.headers[]|.[0]]' "$OUT/$n.json" 2>/dev/null || head -c 200 "$OUT/$n.json")"
done

# 2g. the app's own cookies arrive as the browser sent them.
curl -sk -H 'Cookie: app_json={"a":1}; app_bs=a\b; app_utf=héllo; app_eq=abc==; grain_session=secret-x; app_ok=1' \
	"$P/staging/preview/echo" -o "$OUT/cookie-fidelity.json"
# http.server decodes header bytes as Latin-1; undo that to compare bytes.
got=$(jq -r '.cookieLines | join("|")' "$OUT/cookie-fidelity.json" | python3 -c 'import sys; sys.stdout.write(sys.stdin.read().encode("latin-1").decode())')
want='app_json={"a":1}; app_bs=a\b; app_utf=héllo; app_eq=abc==; app_ok=1'
if [[ $got == "$want" ]]; then echo "PASS cookie-fidelity: $got"; else echo "FAIL cookie-fidelity: got [$got] want [$want]"; FAILS=$((FAILS + 1)); fi

# 2f. WebSocket upgrade, HTTP/1.1 (the only upgrade ReverseProxy carries).
python3 - "$OUT" "${COOKIES}" <<'PY'
import socket, ssl, sys, re, base64, os, json
out, cookies = sys.argv[1], sys.argv[2]
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
s = ctx.wrap_socket(socket.create_connection(("127.0.0.1", 4292)), server_hostname="localhost")
key = base64.b64encode(os.urandom(16)).decode()
req = ("GET /staging/preview/ws HTTP/1.1\r\nHost: 127.0.0.1:4292\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
       f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nOrigin: https://127.0.0.1:4292\r\n"
       "Authorization: Bearer secret-ws\r\nX-Goog-IAP-JWT-Assertion: secret-wsjwt\r\nX-Grain-Access-Code: secret-wscode\r\n"
       f"Cookie: {cookies}\r\n\r\n")
s.sendall(req.encode())
data = b""
while True:
    b = s.recv(65536)
    if not b: break
    data += b
head, _, rest = data.partition(b"\r\n\r\n")
lines = head.decode().split("\r\n")
set_cookies = [l.split(":", 1)[1].strip() for l in lines if l.lower().startswith("set-cookie:")]
# one server text frame: 0x81, len
n = rest[1] & 0x7f; off = 2
if n == 126: n = int.from_bytes(rest[2:4], "big"); off = 4
elif n == 127: n = int.from_bytes(rest[2:10], "big"); off = 10
payload = rest[off:off+n]
payload = re.sub(rb"secret-[A-Za-z0-9._-]*", b"secret-REDACTED", payload)
payload = re.sub(rb"v1\|[^\"|]*\|[^\"|]*\|[0-9]+\|[0-9a-f]{64}", b"v1|REDACTED", payload)
set_cookies = [re.sub(r"(__Host-grain_verified=)v1\.[A-Za-z0-9._-]*", r"\1REDACTED", c) for c in set_cookies]
open(f"{out}/websocket.json", "wb").write(payload)
open(f"{out}/websocket.ver", "w").write("1.1 " + lines[0])
open(f"{out}/websocket.setcookie", "w").write("\n".join(set_cookies) + "\n")
PY
check "websocket ($(cat "$OUT/websocket.ver"))" "$OUT/websocket.json"
echo "INFO websocket 101 Set-Cookie: $(tr '\n' '|' <"$OUT/websocket.setcookie")"

# 1b. planted cookies: what reaches the browser.
curl -sk -D "$OUT/plant.headers" -o /dev/null "$P/staging/preview/plant"
grep -i '^set-cookie:\|^clear-site-data:\|^content-security-policy:' "$OUT/plant.headers" | tr -d '\r' | redact >"$OUT/plant.kept"
# Only the app's cookies carry "planted-" values; grain's own re-issued
# __Host-grain_verified session (redacted) is expected on every response.
planted=$(grep -iE '^set-cookie:.*=planted-' "$OUT/plant.kept" || true)
if grep -iqE '^set-cookie: *((__secure-)?(grain_|gcp_iap|gcp_iaap)|__host-)[^=]*=planted-' "$OUT/plant.kept" ||
	grep -iqE '^clear-site-data' "$OUT/plant.kept" ||
	grep -iE '^set-cookie:' "$OUT/plant.kept" | grep -v 'grain_verified=REDACTED' | grep -viqE 'path=/staging/preview' ||
	grep -iqE '^set-cookie:.*domain=' "$OUT/plant.kept"; then
	echo "FAIL plant: $(tr '\n' '|' <"$OUT/plant.kept")"
	FAILS=$((FAILS + 1))
else
	echo "PASS plant: $(grep -c . <<<"$planted") app cookies kept, all Path=/staging/preview, no Domain, no Clear-Site-Data"
fi

echo "FAILS=$FAILS"
exit "$FAILS"
