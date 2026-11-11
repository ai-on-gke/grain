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

# 2026-11-10 §7 item 2: every pre-rename spelling, once, against a HEAD hub
# (interop.sh PAIR=new-hub KEEP=1 HUB_ONLY=1) and HEAD grain/grain-hub. For
# each: does it work, how many deprecation warnings does it print, and does
# the new spelling win when both are given.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
NEW="${NEW:-/tmp/bin-new}"
G="$NEW/grain"
W=/tmp/interop-new-hub
A=/tmp/alias-walk
TAP=http://127.0.0.1:4230
BAD=http://127.0.0.1:4299
PAIR=new-hub KEEP=1 HUB_ONLY=1 "$HERE/interop.sh" >/dev/null 2>&1
trap 'pkill -f "$A/" 2>/dev/null; pkill -f "$W/" 2>/dev/null; /usr/lib/postgresql/*/bin/pg_ctl -D $W/pg -m immediate stop >/dev/null 2>&1' EXIT
rm -rf "$A" && mkdir -p "$A"
cp "$G" "$A/grain"
G="$A/grain"
unset GRAIN_SERVER GRAIN_TOKEN
for v in $(env | grep -o '^GRAIN_\(HUB\|REGISTRY\)_[A-Z_]*'); do unset "$v"; done
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
fakejwt() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
TOK="$(fakejwt alice@example.com)"
warns() { grep -c -i 'deprecated' "$1"; }
row() { # row <label> <exit> <outfile> [extra]
  printf '  %-72s exit=%s warnings=%s %s\n' "$1" "$2" "$(warns "$3")" "${4:-}"
  grep -i deprecated "$3" | sed -E 's/^[0-9\/]+ [0-9:]+ //; s/^/      | /' | sort | uniq -c | sed 's/^ *\([0-9]*\) *      | /      | x\1 /'
}
cli() { # cli <label> <env...> -- <args...>
  local label=$1
  shift
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  env "${envs[@]}" "$G" "$@" >"$A/out" 2>&1
  local rc=$?
  row "$label" "$rc" "$A/out" "$(grep -c '^conn-\|^seed-' "$A/out") entries"
}

echo "=== help text ==="
for args in "help hub" "help registry" "hub -h" "registry -h"; do
  "$G" $args >"$A/out" 2>&1
  printf '  grain %-14s exit=%s "Grain Hub"=%s "Grain Registry"=%s warnings=%s first: %s\n' "$args" $? \
    "$(grep -c 'Grain Hub' "$A/out")" "$(grep -c 'Grain Registry' "$A/out")" "$(warns "$A/out")" "$(grep -m1 -i 'grain hub' "$A/out" | cut -c1-90)"
done

echo "=== seed an entry so list has something to show ==="
"$G" hub register -standalone -hub-url $TAP -hub-token "$TOK" -mint-token -token-file "$A/conn.token" -listed conn-alias >/dev/null 2>&1
echo "  registered conn-alias: $(head -c 4 "$A/conn.token")…"

echo "=== CLI command word, flags and env vars (grain hub list) ==="
cli "grain hub list -hub-url -hub-token (baseline)" -- hub list -hub-url $TAP -hub-token "$TOK"
cli "grain registry list -hub-url -hub-token" -- registry list -hub-url $TAP -hub-token "$TOK"
cli "grain hub list -registry-url -registry-token" -- hub list -registry-url $TAP -registry-token "$TOK"
cli "grain hub list -hub-url good -registry-url bad (new wins)" -- hub list -registry-url $BAD -hub-url $TAP -hub-token "$TOK"
cli "grain hub list -hub-url bad -registry-url good (new wins: fails)" -- hub list -hub-url $BAD -registry-url $TAP -hub-token "$TOK"
cli "grain registry list -registry-url -registry-token (3 spellings)" -- registry list -registry-url $TAP -registry-token "$TOK"
cli "\$GRAIN_HUB_URL/\$GRAIN_HUB_TOKEN (baseline)" GRAIN_HUB_URL=$TAP GRAIN_HUB_TOKEN="$TOK" -- hub list
cli "\$GRAIN_REGISTRY_URL/\$GRAIN_REGISTRY_TOKEN" GRAIN_REGISTRY_URL=$TAP GRAIN_REGISTRY_TOKEN="$TOK" -- hub list
cli "\$GRAIN_HUB_URL good + \$GRAIN_REGISTRY_URL bad (new wins)" GRAIN_HUB_URL=$TAP GRAIN_REGISTRY_URL=$BAD GRAIN_HUB_TOKEN="$TOK" -- hub list
cli "\$GRAIN_HUB_URL bad + \$GRAIN_REGISTRY_URL good (new wins: fails)" GRAIN_HUB_URL=$BAD GRAIN_REGISTRY_URL=$TAP GRAIN_HUB_TOKEN="$TOK" -- hub list

echo "=== grain-hub: \$GRAIN_REGISTRY_CONFIG, compute.registry_id, the grain-registry shim ==="
env GRAIN_REGISTRY_CONFIG="$W/hub.yaml" "$NEW/grain-hub" validate >"$A/out" 2>&1
row "GRAIN_REGISTRY_CONFIG=hub.yaml grain-hub validate" $? "$A/out" "$(grep -o 'is valid' "$A/out")"
env GRAIN_HUB_CONFIG="$W/hub.yaml" GRAIN_REGISTRY_CONFIG=/nonexistent.yaml "$NEW/grain-hub" validate >"$A/out" 2>&1
row "GRAIN_HUB_CONFIG good + GRAIN_REGISTRY_CONFIG missing (new wins)" $? "$A/out" "$(grep -o 'is valid' "$A/out")"
sed 's/^  provider: none$/  provider: none\n  registry_id: walk-old/' "$W/hub.yaml" >"$A/hub-regid.yaml"
"$NEW/grain-hub" validate -config "$A/hub-regid.yaml" >"$A/out" 2>&1
row "compute.registry_id: walk-old  grain-hub validate" $? "$A/out" "$(grep -o 'is valid' "$A/out")"
sed 's/^  provider: none$/  provider: none\n  registry_id: walk-old\n  hub_id: walk-new/' "$W/hub.yaml" >"$A/hub-both.yaml"
"$NEW/grain-hub" validate -config "$A/hub-both.yaml" >"$A/out" 2>&1
row "compute.registry_id + compute.hub_id  grain-hub validate" $? "$A/out" "$(grep -o 'is valid' "$A/out")"
"$NEW/grain-registry" validate -config "$W/hub.yaml" >"$A/out" 2>&1
row "grain-registry (cmd/grain-registry shim) validate" $? "$A/out" "$(grep -o 'is valid' "$A/out")"
"$NEW/grain-hub" validate -config "$W/hub.yaml" >"$A/out" 2>&1
row "grain-hub validate (baseline)" $? "$A/out" "$(grep -o 'is valid' "$A/out")"

daemon() { # daemon <label> <env...> -- <flags...>: start a HEAD daemon, wait for the tunnel, assert identity
  local label=$1
  shift
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  pkill -f "$A/grain daemon" 2>/dev/null
  sleep 1
  rm -rf "$A/d" && mkdir -p "$A/d/scratch"
  env "${envs[@]}" "$G" daemon -data-dir "$A/d" -sandbox-dir "$A/d/scratch" -disable-native-sandboxes -sandbox-port-base=0 -ci=false \
    -deployment-owner=alice@example.com -ui-allowed-principals=user:alice@example.com \
    -ui-addr=127.0.0.1:4271 -ui-allowed-hosts=conn-alias.grain-user.test,127.0.0.1,localhost -ui-require-verifier=registry "$@" >"$A/daemon.log" 2>&1 &
  local code=000
  for _ in $(seq 1 80); do
    code=$(curl -sS -o "$A/whoami.json" -w '%{http_code}' -H 'Host: conn-alias.grain-user.test' -H 'X-Test-Email: alice@example.com' $TAP/api/whoami 2>/dev/null)
    [ "$code" = 200 ] && break
    sleep 0.5
  done
  grep -i 'deprecated' "$A/daemon.log" | grep -v -E 'ui-allowed-principals|hostd-token' >"$A/dwarn"
  row "$label" "-" "$A/dwarn" "tunnel whoami HTTP $code actor=$(jq -r .actor.id "$A/whoami.json" 2>/dev/null)"
}
J=http://127.0.0.1:4233/.well-known/jwks.json
AUDE=https://conn-alias.grain-user.test
echo "=== grain daemon: -hub-*, -registry-*, -ui-registry-* and \$GRAIN_REGISTRY_* ==="
daemon "daemon -hub-* -ui-hub-* (baseline)" -- -hub-url $TAP -hub-entry conn-alias -hub-token-file "$A/conn.token" -hub-connect \
  -ui-hub-issuer=grain-registry -ui-hub-audience=$AUDE -ui-hub-keys-url=$J
daemon "daemon -registry-url/-entry/-token-file/-connect -ui-registry-issuer/-audience/-keys-url" -- \
  -registry-url $TAP -registry-entry conn-alias -registry-token-file "$A/conn.token" -registry-connect \
  -ui-registry-issuer=grain-registry -ui-registry-audience=$AUDE -ui-registry-keys-url=$J
daemon "daemon -hub-entry conn-alias -registry-entry nope -ui-hub-audience good -ui-registry-audience bad (new wins)" -- \
  -hub-url $TAP -registry-entry nope -hub-entry conn-alias -hub-token-file "$A/conn.token" -hub-connect \
  -ui-hub-issuer=grain-registry -ui-registry-audience=https://nope.grain-user.test -ui-hub-audience=$AUDE -ui-hub-keys-url=$J
daemon "daemon \$GRAIN_REGISTRY_{URL,ENTRY,TOKEN,CONNECT,ISSUER,AUDIENCE,KEYS_URL}, no hub flags" \
  GRAIN_REGISTRY_URL=$TAP GRAIN_REGISTRY_ENTRY=conn-alias GRAIN_REGISTRY_TOKEN="$(cat "$A/conn.token")" GRAIN_REGISTRY_CONNECT=1 \
  GRAIN_REGISTRY_ISSUER=grain-registry GRAIN_REGISTRY_AUDIENCE=$AUDE GRAIN_REGISTRY_KEYS_URL=$J --
daemon "daemon \$GRAIN_HUB_ENTRY good + \$GRAIN_REGISTRY_ENTRY bad, \$GRAIN_HUB_AUDIENCE good + old bad (new wins)" \
  GRAIN_HUB_URL=$TAP GRAIN_HUB_ENTRY=conn-alias GRAIN_REGISTRY_ENTRY=nope GRAIN_HUB_TOKEN="$(cat "$A/conn.token")" GRAIN_HUB_CONNECT=1 \
  GRAIN_HUB_ISSUER=grain-registry GRAIN_HUB_AUDIENCE=$AUDE GRAIN_REGISTRY_AUDIENCE=https://nope.grain-user.test GRAIN_HUB_KEYS_URL=$J --

echo "=== settings API key \"registry\" -> \"hub\" (PUT/GET /api/settings on a plain HEAD daemon) ==="
pkill -f "$A/grain daemon" 2>/dev/null
sleep 1
rm -rf "$A/s" && mkdir -p "$A/s/scratch"
"$G" daemon -data-dir "$A/s" -sandbox-dir "$A/s/scratch" -disable-native-sandboxes -sandbox-port-base=0 -ci=false \
  -deployment-owner=alice@example.com -ui-addr=127.0.0.1:4272 -hub-allow-insecure-http >"$A/settings-daemon.log" 2>&1 &
for _ in $(seq 1 80); do curl -fsS -o /dev/null http://127.0.0.1:4272/healthz 2>/dev/null && break; sleep 0.5; done
[ -s "$A/s/local-api-token" ] || "$G" daemon -h 2>&1 | grep -q hub-allow-insecure-http || echo "  (no -hub-allow-insecure-http flag)"
AT="$(cat "$A/s/local-api-token" 2>/dev/null)"
put() { # put <label> <json>
  local code
  code=$(curl -sS -o "$A/put.json" -w '%{http_code}' -X PUT -H "Authorization: Bearer $AT" -H 'Content-Type: application/json' -d "$2" http://127.0.0.1:4272/api/settings)
  curl -sS -H "Authorization: Bearer $AT" http://127.0.0.1:4272/api/settings >"$A/get.json"
  printf '  %-58s PUT HTTP %s -> hub.url=%s registry.url=%s hub.entry=%s\n' "$1" "$code" \
    "$(jq -r '.hub.url // "-"' "$A/get.json")" "$(jq -r '.registry.url // "-"' "$A/get.json")" "$(jq -r '.hub.entry // "-"' "$A/get.json")"
  [ "$code" = 200 ] || head -c 300 "$A/put.json" | sed 's/^/      | /'
}
put '{"hub": {...}} (baseline)' '{"hub":{"url":"https://hub-new.example.com","entry":"walk-new"}}'
put '{"registry": {...}}' '{"registry":{"url":"https://hub-old.example.com","entry":"walk-old"}}'
put '{"registry": old, "hub": new} (new wins)' '{"registry":{"url":"https://hub-old.example.com","entry":"walk-old"},"hub":{"url":"https://hub-new.example.com","entry":"walk-new"}}'
put '{"hub": new, "registry": old} (new wins, order swapped)' '{"hub":{"url":"https://hub-new.example.com","entry":"walk-new"},"registry":{"url":"https://hub-old.example.com","entry":"walk-old"}}'
echo "  daemon log deprecation lines for the settings key: $(grep -i deprecated "$A/settings-daemon.log" | grep -c -i -E 'settings|"registry"')"

echo "=== demo: -ui-registry-* ==="
pkill -f "$A/grain daemon" 2>/dev/null
"$G" demo -addr 127.0.0.1:4273 -ui-registry-issuer=grain-registry -ui-registry-audience=https://demo.grain-user.test -ui-registry-keys-url=$J >"$A/demo.log" 2>&1 &
for _ in $(seq 1 80); do curl -fsS -o /dev/null http://127.0.0.1:4273/healthz 2>/dev/null && break; sleep 0.5; done
grep -i deprecated "$A/demo.log" >"$A/dwarn"
row "grain demo -ui-registry-issuer/-audience/-keys-url" "-" "$A/dwarn" "healthz HTTP $(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:4273/healthz)"
pkill -f "$A/grain demo" 2>/dev/null
true
