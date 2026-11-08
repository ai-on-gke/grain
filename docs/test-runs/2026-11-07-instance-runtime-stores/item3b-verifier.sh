#!/bin/bash
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

# §2 item 3b: generalized -ui-require-verifier (#742) on a live daemon and
# `grain access-code`'s stderr note, including a hostile verifier label.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
DB=walk_i3b; URL=$(pgurl $DB); D=$W/i3b
rm -rf "$W/i3b"*; recreate_db $DB
mkdir -p "$W/i3b-jwks"; echo '{"keys":[]}' >"$W/i3b-jwks/jwks.json"
python3 -m http.server 4308 --bind 127.0.0.1 --directory "$W/i3b-jwks" >/dev/null 2>&1 &
JW=$!; sleep 1
REG=(-ui-registry-issuer https://registry.example.test -ui-registry-audience https://calm-otter.grain.example.test -ui-registry-keys-url http://127.0.0.1:4308/jwks.json)

say "3b-1. startup validation"
for args in "-ui-require-verifier bogus" "-ui-require-verifier registry" "-ui-require-iap -ui-require-verifier registry ${REG[*]}" "-ui-require-verifier iap"; do
  P=$(daemon i3b-bad 4307 "$W/i3b-bad" -postgres-url "$URL" $args); wait_gone "$P"
  printf '%-40.40s -> %s\n' "$args" "$(grep -m1 -o 'ui-require-.*' "$W/i3b-bad.log" | cut -c1-200)"
done

say "3b-2. -ui-require-verifier=registry with the registry verifier configured"
P=$(daemon i3b 4307 "$D" -postgres-url "$URL" -ui-require-verifier registry "${REG[@]}")
wait_up 4307 || { tail -20 "$W/i3b.log"; exit 1; }
T=$(tok "$D")
curl -s -o /dev/null -w 'loopback bearer, no forwarding headers: %{http_code}\n' -H "Authorization: Bearer $T" http://127.0.0.1:4307/api/tasks
curl -s -o /dev/null -w 'same bearer with X-Forwarded-For (a proxied network caller): %{http_code}\n' -H "Authorization: Bearer $T" -H 'X-Forwarded-For: 203.0.113.9' http://127.0.0.1:4307/api/tasks
curl -s -o /dev/null -w 'grain_access cookie with X-Forwarded-For: %{http_code}\n' -b "grain_access=$T" -H 'X-Forwarded-For: 203.0.113.9' http://127.0.0.1:4307/api/tasks
curl -s -H 'X-Forwarded-For: 203.0.113.9' http://127.0.0.1:4307/ | grep -c -i "enter access code" | sed 's/^/"Enter access code" page served to a network caller: /'
curl -s http://127.0.0.1:4307/api/auth/access-code | jq -c .
echo "grain access-code (stdout is the code, the note goes to stderr):"
GRAIN_SERVER=http://127.0.0.1:4307 "$GRAIN" access-code -data-dir "$D" 2>"$W/i3b-ac.err" | sed 's/./x/g' | sed 's/^/  stdout: /'; sed 's/^/  stderr: /' "$W/i3b-ac.err"
kill "$P"; wait_gone "$P"

say "3b-3. a server answering with a hostile verifier label"
python3 - <<'PY' >/dev/null 2>&1 &
import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        b = json.dumps({"requireVerifier": "registry\u001b]0;pwned\u0007\u001b[2J"}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(b)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", 4309), H).serve_forever()
PY
HS=$!; sleep 1
GRAIN_SERVER=http://127.0.0.1:4309 "$GRAIN" access-code -data-dir "$D" 2>"$W/i3b-ac2.err" >/dev/null
echo "  stderr: $(cat "$W/i3b-ac2.err")"
echo "  escape bytes on stderr: $(tr -cd '\033\007' <"$W/i3b-ac2.err" | wc -c)"
kill $HS $JW
