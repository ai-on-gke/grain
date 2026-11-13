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

# §13 item 2: a real grain-hub (PostgreSQL store, `compute: none`, IAP in
# front played by docs/test-runs/2026-11-07-grain-registry/iapstub.go) and a
# real connected `grain daemon -sandbox-port-base=0`. Checks that
#   a. the connected entry's display name becomes the daemon's environment
#      name on the first heartbeat;
#   b. renaming the entry renames the grain on the next heartbeat;
#   c. a name the operator set in Settings survives a further rename;
#   d. a hosted-style daemon started with GRAIN_DEFAULT_ENVIRONMENT_NAME takes
#      it, and an operator-set name survives a restart with a new default.
# Nothing cloud-side is created: `compute: none` means the hub provisions no
# VM, which is the only thing stubbed.
#
# Ports (42xx): PostgreSQL :4240, IAP JWKS :4250, IAP front :4231, hub :4232
# (jwks :4233, peer :4234), daemons :4270 and :4271.
#   BIN=/home/debian/bin hub-naming.sh   # needs grain, grain-hub, iapstub
set -uo pipefail
BIN="${BIN:-/home/debian/bin}"
W="${W:-/tmp/hub-naming}"
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
AUD=/projects/walk/global/backendServices/hub
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_HUB_URL GRAIN_HUB_TOKEN

cleanup() {
  pkill -f "$W/" 2>/dev/null || true
  [ -d "$W/pg" ] && "$PGBIN/pg_ctl" -D "$W/pg" -m immediate stop >/dev/null 2>&1 || true
}
[ "${KEEP:-0}" = 1 ] || trap cleanup EXIT
cleanup
rm -rf "$W"
mkdir -p "$W/logs" "$W/keys" "$W/bin" "$W/d1/scratch" "$W/d2/scratch"
cp "$BIN/grain" "$BIN/grain-hub" "$BIN/iapstub" "$W/bin/"

"$PGBIN/initdb" -D "$W/pg" -U postgres --auth=trust --no-instructions --encoding=UTF8 --locale=C >/dev/null
"$PGBIN/pg_ctl" -D "$W/pg" -l "$W/logs/pg.log" -o "-p 4240 -k $W -c listen_addresses=127.0.0.1" -w start >/dev/null
psql -h 127.0.0.1 -p 4240 -U postgres -qc "CREATE DATABASE hub" >/dev/null
openssl ecparam -name prime256v1 -genkey -noout -out "$W/keys/hub.pem"
nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -jwks-addr 127.0.0.1:4250 >>"$W/logs/iap.log" 2>&1 &
sleep 0.5
nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -addr 127.0.0.1:4231 -upstream http://127.0.0.1:4232 -audience $AUD >>"$W/logs/iap.log" 2>&1 &

cat >"$W/hub.yaml" <<EOF
listen: "127.0.0.1:4232"
jwks_listen: "127.0.0.1:4233"
host: hub.walk.test
instance_domain: grain-user.test
auth:
  provider: iap
  iap_audience: $AUD
  iap_certs_url: http://127.0.0.1:4250/jwks
  allowed_domains: [example.com]
  admins: [user:root@example.com]
groups:
  provider: static
signing:
  provider: local
  key_file: $W/keys/hub.pem
  key_id: walk-1
  jwt_ttl: 60s
catalog:
  heartbeat_interval: 2s
tunnel:
  transport: ws-mux
  peer_listen: "127.0.0.1:4234"
database:
  url: postgres://postgres@127.0.0.1:4240/hub?sslmode=disable
compute:
  provider: none
EOF
nohup "$W/bin/grain-hub" serve -config "$W/hub.yaml" >>"$W/logs/hub.log" 2>&1 &
for _ in $(seq 1 50); do curl -fsS http://127.0.0.1:4233/healthz >/dev/null 2>&1 && break; sleep 0.2; done

HUB=http://127.0.0.1:4231
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
fakejwt() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
hubapi() { curl -sS -H 'X-Test-Email: alice@example.com' -H 'Content-Type: application/json' "$@"; }
# envname <port>: the daemon's environment name as its own Settings reports it.
code() { "$W/bin/grain" access-code -data-dir "$W/$([ "$1" = 4270 ] && echo d1 || echo d2)" show 2>/dev/null | head -n 1; }
dapi() { local port=$1; shift; curl -sS -H "Authorization: Bearer $(code "$port")" "$@"; }
envname() { dapi "$1" "http://127.0.0.1:$1/api/settings" | jq -r '.environmentName'; }
waitname() { # waitname <port> <want> <seconds>
  local t0=$SECONDS
  for _ in $(seq 1 "$3"); do [ "$(envname "$1")" = "$2" ] && { echo "  \"$2\" after $((SECONDS - t0))s"; return 0; }; sleep 1; done
  return 1
}
check() { if "$@"; then echo "  PASS"; else echo "  FAIL (now: \"$(envname "${2}")\")"; fi; }

echo "--- register connected entry name-walk (display name \"Name Walk\")"
"$W/bin/grain" hub register -standalone -hub-url "$HUB" -hub-token "$(fakejwt alice@example.com)" \
  -mint-token -token-file "$W/name-walk.token" -display-name "Name Walk" name-walk 2>&1 | grep -v -i grc_ | sed 's/^/  /'

echo "--- a. start the connected daemon; first heartbeat names it"
nohup "$W/bin/grain" daemon -data-dir "$W/d1" -sandbox-dir "$W/d1/scratch" -disable-native-sandboxes -sandbox-port-base=0 -ci=false \
  -deployment-owner=alice@example.com -ui-addr=127.0.0.1:4270 \
  -hub-url "$HUB" -hub-entry name-walk -hub-token-file "$W/name-walk.token" >>"$W/logs/d1.log" 2>&1 &
for _ in $(seq 1 120); do curl -fsS -o /dev/null http://127.0.0.1:4270/healthz 2>/dev/null && break; sleep 0.5; done
echo "  before any heartbeat could land: \"$(envname 4270)\""
check waitname 4270 "Name Walk" 120

echo "--- b. rename the entry on the hub; the next heartbeat renames the grain"
hubapi -X PATCH "$HUB/api/entries/name-walk" -d '{"display_name":"Renamed Walk"}' -o /dev/null -w '  PATCH display_name HTTP %{http_code}\n'
check waitname 4270 "Renamed Walk" 120

echo "--- c. the operator names it in Settings; a further hub rename leaves it alone"
dapi 4270 -X PUT -H 'Content-Type: application/json' \
  http://127.0.0.1:4270/api/settings -d '{"environmentName":"Operator Name"}' -o "$W/put.json" -w '  PUT /api/settings HTTP %{http_code}\n'
hubapi -X PATCH "$HUB/api/entries/name-walk" -d '{"display_name":"Hub Rename 3"}' -o /dev/null -w '  PATCH display_name HTTP %{http_code}\n'
sleep 70
echo "  after 70s: \"$(envname 4270)\""
check waitname 4270 "Operator Name" 1
echo "  hub entry display_name: $(hubapi "$HUB/api/entries/name-walk" | jq -r .display_name)"

echo "--- d. hosted-style default: GRAIN_DEFAULT_ENVIRONMENT_NAME"
start_d2() {
  GRAIN_DEFAULT_ENVIRONMENT_NAME="$1" nohup "$W/bin/grain" daemon -data-dir "$W/d2" -sandbox-dir "$W/d2/scratch" -disable-native-sandboxes \
    -sandbox-port-base=0 -ci=false -deployment-owner=alice@example.com -ui-addr=127.0.0.1:4271 >>"$W/logs/d2.log" 2>&1 &
  for _ in $(seq 1 120); do curl -fsS -o /dev/null http://127.0.0.1:4271/healthz 2>/dev/null && break; sleep 0.5; done
}
stop_d2() { pkill -f "$W/d2" ; for _ in $(seq 1 40); do curl -fsS -o /dev/null http://127.0.0.1:4271/healthz 2>/dev/null || break; sleep 0.5; done; }
start_d2 "Hosted Default"
check waitname 4271 "Hosted Default" 5
stop_d2; start_d2 "Hosted Renamed"
check waitname 4271 "Hosted Renamed" 5
dapi 4271 -X PUT -H 'Content-Type: application/json' \
  http://127.0.0.1:4271/api/settings -d '{"environmentName":"Hosted Operator"}' -o /dev/null -w '  PUT /api/settings HTTP %{http_code}\n'
stop_d2; start_d2 "Hosted Third"
check waitname 4271 "Hosted Operator" 5
echo "--- daemon log lines about the environment name"
grep -h "environment name" "$W/logs/d1.log" "$W/logs/d2.log" | sed 's/^/  /'
