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

# Environment setup for docs/test-plans/2026-11-04.md §4 walkthrough.
# Starts local git bare repositories (acme/app, acme/other), simulated IAP
# (tests/iap/iapsim.go), and a real `grain daemon` with embedded UI.
set -euo pipefail

ROOT="${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3070}"
GRAIN_BIN="${GRAIN_BIN:-/home/debian/bin/grain}"
REPO_CHECKOUT="${REPO_CHECKOUT:-/home/debian/work}"

UI_ADDR="127.0.0.1:14300"
IAP_ISSUER="127.0.0.1:18180"
IAP_PROXY="127.0.0.1:18443"
IAP_AUD="/projects/123456/global/backendServices/789012"
IAP_ISS="https://iap.test.local"

stop_all() {
  for pidfile in "$ROOT/daemon.pid" "$ROOT/iapsim.pid"; do
    if [[ -f "$pidfile" ]]; then
      pid="$(cat "$pidfile" || true)"
      if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        for _ in $(seq 1 40); do
          kill -0 "$pid" 2>/dev/null || break
          sleep 0.1
        done
      fi
      rm -f "$pidfile"
    fi
  done
}

if [[ "${1:-start}" == "stop" ]]; then
  stop_all
  exit 0
fi

stop_all
rm -rf "$ROOT"
mkdir -p "$ROOT/data" "$ROOT/repos/acme" "$ROOT/logs"

init_bare_repo() {
  local name="$1"
  local bare="$ROOT/repos/acme/${name}.git"
  local work="$ROOT/tmp-${name}"
  git init --bare -b main "$bare" >/dev/null
  git clone "$bare" "$work" >/dev/null 2>&1
  (
    cd "$work"
    git config user.name "Walkthrough Seed"
    git config user.email "seed@example.com"
    echo "# acme/${name}" > README.md
    git add README.md
    git commit -m "chore: initial commit" >/dev/null
    git push origin main >/dev/null 2>&1
  )
  rm -rf "$work"
}

init_bare_repo "app"
init_bare_repo "other"

(
  cd "$REPO_CHECKOUT"
  nohup go run ./tests/iap/iapsim.go \
    -issuer "$IAP_ISSUER" \
    -proxy "$IAP_PROXY" \
    -upstream "http://${UI_ADDR}" \
    -aud "$IAP_AUD" \
    -iss "$IAP_ISS" \
    >"$ROOT/logs/iapsim.log" 2>&1 &
  echo $! >"$ROOT/iapsim.pid"
)

for _ in $(seq 1 50); do
  if curl -fsS "http://${IAP_ISSUER}/jwks" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
curl -fsS "http://${IAP_ISSUER}/jwks" >/dev/null

nohup "$GRAIN_BIN" daemon \
  -data-dir "$ROOT/data" \
  -ui-addr "$UI_ADDR" \
  -sandbox-port-base=0 \
  -disable-native-sandboxes \
  -ci=false \
  -poll-interval 2s \
  -github-host "127.0.0.1:1" \
  -github-insecure-http \
  -as "operator@example.com" \
  -ui-iap-audience "$IAP_AUD" \
  -ui-iap-issuer "$IAP_ISS" \
  -ui-iap-keys-url "http://${IAP_ISSUER}/jwks" \
  -ui-allowed-principals "user:alice@example.com,user:bob@example.com,user:operator@example.com" \
  -repo "acme/app" \
  -repo "acme/other" \
  >"$ROOT/logs/daemon.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 80); do
  if curl -fsS "http://${UI_ADDR}/api/auth/access-code" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
curl -fsS "http://${UI_ADDR}/api/auth/access-code" >/dev/null

ACCESS_CODE="$(tr -d '\r\n' < "$ROOT/data/local-api-token")"

# Seed a git connection on the stubbed -github-host (127.0.0.1:1) so repo reach
# checks treat acme/app and acme/other as reachable rather than parking filings
# in awaiting_reply for lack of a GitHub credential, and turn on externalAgentApi.
curl -fsS -X POST "http://${UI_ADDR}/api/deployment/git-connections" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"type":"pat","host":"127.0.0.1:1","value":"grain-walkthrough-stub"}' >/dev/null

curl -fsS -X PUT "http://${UI_ADDR}/api/settings" \
  -H "X-Grain-Access-Code: ${ACCESS_CODE}" \
  -H "Content-Type: application/json" \
  -d '{"externalAgentApi":true}' >/dev/null
cat >"$ROOT/vars.env" <<EOF
export WALKTHROUGH_DIR="$ROOT"
export GRAIN_BIN="$GRAIN_BIN"
export UI_URL="http://${UI_ADDR}"
export IAP_URL="http://${IAP_PROXY}"
export IAP_ISSUER_URL="http://${IAP_ISSUER}"
export ACCESS_CODE="${ACCESS_CODE}"
export DAEMON_LOG="$ROOT/logs/daemon.log"
export STORE_DB="$ROOT/data/store/grain.db"
EOF

echo "Ready: UI_URL=http://${UI_ADDR} IAP_URL=http://${IAP_PROXY} daemon_pid=$(cat "$ROOT/daemon.pid")"
