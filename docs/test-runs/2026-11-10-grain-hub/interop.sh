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

# 2026-11-10 §7 item 1: mixed-version interop between a Grain Hub and a
# connected grain daemon, one pairing per run:
#
#   PAIR=old-hub  interop.sh   # c08832a2 grain-registry  + HEAD grain daemon (-hub-* flags)
#   PAIR=new-hub  interop.sh   # HEAD grain-hub           + c08832a2 grain daemon (-registry-* flags)
#   PAIR=new-hub KEEP=1 HUB_ONLY=1 interop.sh  # just leave the HEAD hub running (alias.sh)
#
# Topology (42xx ports), PostgreSQL hub store, compute: none:
#   daemon/CLI -> hdrtap :4230 -> iapstub :4231 -> hub :4232 (jwks :4233, peer :4234)
#   PostgreSQL :4240, IAP JWKS :4250, daemon UI :4270
# The daemon registers its entry with its own version's CLI, opens the
# outbound ws-mux tunnel, and alice's request to conn.grain-user.test is
# forwarded through the tunnel with the hub's assertion, which the daemon
# verifies (-ui-require-verifier=registry). echoconn (a HEAD-built connector)
# then reports which X-Grain-* headers that hub forwards. hdrtap logs which
# headers the daemon and CLI send to the hub.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
PAIR="${PAIR:?PAIR=old-hub or PAIR=new-hub}"
NEW="${NEW:-/tmp/bin-new}"
OLD="${OLD:-/tmp/bin-old}"
W="${W:-/tmp/interop-$PAIR}"
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
AUD=/projects/walk/global/backendServices/hub
case "$PAIR" in
old-hub) HUB="$OLD/grain-registry" G="$NEW/grain" CMD=hub P=hub ;;
new-hub) HUB="$NEW/grain-hub" G="$OLD/grain" CMD=registry P=registry ;;
*) echo "PAIR must be old-hub or new-hub" >&2; exit 2 ;;
esac
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_HUB_URL GRAIN_HUB_TOKEN GRAIN_REGISTRY_URL GRAIN_REGISTRY_TOKEN

cleanup() {
  pkill -f "$W/" 2>/dev/null || true
  [ -d "$W/pg" ] && "$PGBIN/pg_ctl" -D "$W/pg" -m immediate stop >/dev/null 2>&1 || true
}
[ "${KEEP:-0}" = 1 ] || trap cleanup EXIT
cleanup
rm -rf "$W"
mkdir -p "$W/logs" "$W/keys" "$W/d/scratch" "$W/bin"
cp /tmp/hdrtap /tmp/echoconn /tmp/iapstub "$W/bin/"

"$PGBIN/initdb" -D "$W/pg" -U postgres --auth=trust --no-instructions --encoding=UTF8 --locale=C >/dev/null
"$PGBIN/pg_ctl" -D "$W/pg" -l "$W/logs/pg.log" -o "-p 4240 -k $W -c listen_addresses=127.0.0.1" -w start >/dev/null
psql -h 127.0.0.1 -p 4240 -U postgres -qc "CREATE DATABASE hub" >/dev/null
openssl ecparam -name prime256v1 -genkey -noout -out "$W/keys/hub.pem"

nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -jwks-addr 127.0.0.1:4250 >>"$W/logs/iap.log" 2>&1 &
sleep 0.5
nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -addr 127.0.0.1:4231 -upstream http://127.0.0.1:4232 -audience $AUD >>"$W/logs/iap.log" 2>&1 &
nohup "$W/bin/hdrtap" -label "$PAIR" -addr 127.0.0.1:4230 -upstream http://127.0.0.1:4231 >>"$W/logs/tap.log" 2>&1 &

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
echo "=== PAIR=$PAIR hub=$(basename "$HUB") ($("$HUB" version 2>/dev/null | head -n 1)) daemon=$G ($("$G" version 2>/dev/null | head -n 1)) ==="
"$HUB" validate -config "$W/hub.yaml" 2>&1 | sed 's/^/  validate: /'
nohup "$HUB" serve -config "$W/hub.yaml" >>"$W/logs/hub.log" 2>&1 &
for _ in $(seq 1 50); do curl -fsS http://127.0.0.1:4233/healthz >/dev/null 2>&1 && break; sleep 0.2; done

b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
fakejwt() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
TAP=http://127.0.0.1:4230
if [ "${HUB_ONLY:-0}" = 1 ]; then echo "hub up under $W (KEEP=${KEEP:-0})"; exit 0; fi

echo "--- register conn-alice and echo with the daemon's own CLI (grain $CMD register -$P-url/-$P-token)"
for e in conn-alice echo; do
  "$G" $CMD register -standalone -$P-url "$TAP" -$P-token "$(fakejwt alice@example.com)" \
    -mint-token -token-file "$W/$e.token" -listed $e 2>&1 | grep -v -i token | sed 's/^/  /'
  echo "  register $e exit=${PIPESTATUS[0]} token=$(head -c 4 "$W/$e.token" 2>/dev/null)…"
done

echo "--- start the daemon with -$P-* flags and the registry verifier"
nohup "$G" daemon -data-dir "$W/d" -sandbox-dir "$W/d/scratch" -disable-native-sandboxes -sandbox-port-base=0 -ci=false \
  -deployment-owner=alice@example.com -ui-allowed-principals=user:alice@example.com \
  -ui-addr=127.0.0.1:4270 -ui-allowed-hosts=conn-alice.grain-user.test,127.0.0.1,localhost \
  -$P-url "$TAP" -$P-entry conn-alice -$P-token-file "$W/conn-alice.token" -$P-connect \
  -ui-require-verifier=registry -ui-$P-issuer=grain-registry -ui-$P-audience=https://conn-alice.grain-user.test \
  -ui-$P-keys-url=http://127.0.0.1:4233/.well-known/jwks.json >>"$W/logs/daemon.log" 2>&1 &
for _ in $(seq 1 120); do
  curl -fsS -o /dev/null http://127.0.0.1:4270/healthz 2>/dev/null && break
  sleep 0.5
done
nohup "$W/bin/echoconn" -hub "$TAP" -entry echo -token-file "$W/echo.token" >>"$W/logs/echo.log" 2>&1 &

st=""
for _ in $(seq 1 60); do
  st=$(curl -sS -H 'X-Test-Email: alice@example.com' http://127.0.0.1:4231/api/entries/conn-alice | jq -r '.status + "/" + (.tunnel_connected|tostring)' 2>/dev/null)
  case "$st" in running/true | online/true) break ;; esac
  sleep 0.5
done
echo "  conn-alice status/tunnel_connected: $st"
curl -sS -H 'X-Test-Email: alice@example.com' http://127.0.0.1:4231/api/entries/conn-alice |
  jq -c '{name,kind,status,reachability,tunnel_connected,grain_version}' 2>/dev/null | sed 's/^/  /'

echo "--- identity assertion through the outbound tunnel (alice -> conn-alice /api/whoami)"
for _ in $(seq 1 20); do
  code=$(curl -sS -o "$W/whoami.json" -w '%{http_code}' -H 'Host: conn-alice.grain-user.test' -H 'X-Test-Email: alice@example.com' $TAP/api/whoami)
  [ "$code" = 200 ] && break
  sleep 0.5
done
echo "  HTTP $code $(jq -c '{actor: .actor.id, source, verified, allowed}' "$W/whoami.json" 2>/dev/null || head -c 300 "$W/whoami.json")"
code=$(curl -sS -o "$W/whoami-bob.json" -w '%{http_code}' -H 'Host: conn-alice.grain-user.test' -H 'X-Test-Email: bob@example.com' $TAP/api/whoami)
echo "  bob (not allowed on the daemon) -> HTTP $code"

echo "--- headers the hub forwards over the tunnel (echoconn)"
for _ in $(seq 1 20); do
  code=$(curl -sS -o "$W/echo.json" -w '%{http_code}' -H 'Host: echo.grain-user.test' -H 'X-Test-Email: alice@example.com' $TAP/probe)
  [ "$code" = 200 ] && break
  sleep 0.5
done
echo "  HTTP $code $(cat "$W/echo.json")"
echo "--- a client-supplied assertion header: does the hub strip or overwrite it?"
for h in X-Grain-Hub-Assertion X-Grain-Registry-Assertion; do
  curl -sS -o "$W/echo-forged.json" -H 'Host: echo.grain-user.test' -H 'X-Test-Email: alice@example.com' -H "$h: forged" $TAP/probe
  echo "  client sends $h: forged -> echoconn sees $(jq -c .x_grain_headers "$W/echo-forged.json")"
  code=$(curl -sS -o "$W/whoami-forged.json" -w '%{http_code}' -H 'Host: conn-alice.grain-user.test' -H 'X-Test-Email: alice@example.com' -H "$h: forged" $TAP/api/whoami)
  echo "  client sends $h: forged -> daemon /api/whoami HTTP $code $(jq -c '{actor: .actor.id, verified}' "$W/whoami-forged.json" 2>/dev/null || head -c 200 "$W/whoami-forged.json")"
done

echo "--- headers the daemon and CLI sent to the hub (hdrtap, distinct method/path/header-name sets)"
sed -E 's/^[0-9\/]+ [0-9:]+ //; s/=[^ ]*//g' "$W/logs/tap.log" | grep -v -E ' (GET|POST) /(probe|api/whoami) ' | sort | uniq -c | sed 's/^/  /'
echo "--- hub metrics prefixes (GET /metrics as admin root@example.com)"
curl -sS -H 'X-Test-Email: root@example.com' http://127.0.0.1:4231/metrics >"$W/metrics.txt"
echo "  grain_hub_* series=$(grep -c '^grain_hub_' "$W/metrics.txt") grain_registry_* series=$(grep -c '^grain_registry_' "$W/metrics.txt")"
grep -E '^grain_(hub|registry)_(active_tunnels|assertions_signed_total) ' "$W/metrics.txt" | sed 's/^/  /'
echo "  alice (not admin) GET /metrics -> HTTP $(curl -sS -o /dev/null -w '%{http_code}' -H 'X-Test-Email: alice@example.com' http://127.0.0.1:4231/metrics)"
echo "--- daemon log lines about the hub"
grep -i -E 'hub|registry|deprecat|tunnel' "$W/logs/daemon.log" | grep -v -i token= | head -n 15 | sed 's/^/  /'
echo "--- hub log errors"
grep -i -E 'error|refus|denied' "$W/logs/hub.log" | head -n 10 | sed 's/^/  /'
