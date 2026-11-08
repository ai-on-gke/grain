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

# Environment for docs/test-plans/2026-11-07.md §4 (Director rails & Agent
# API). Starts a real `grain daemon` (control plane only, SQLite store) over
# two local bare repositories, acme/app and acme/other, and writes vars.env.
#
#   env.sh [start|stop]
set -euo pipefail

ROOT="${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3488}"
GRAIN_BIN="${GRAIN_BIN:-/home/debian/bin/grain}"
UI_ADDR="${UI_ADDR:-127.0.0.1:4271}"
read -r -a EXTRA_FLAGS <<<"${DAEMON_EXTRA_FLAGS:-}"
# The control plane only by default; SANDBOX_FLAGS="-host-sandboxes -sandbox-dir DIR"
# walks the non-isolating backend.
read -r -a SANDBOX_FLAGS <<<"${SANDBOX_FLAGS:--disable-native-sandboxes}"

stop_all() {
  if [[ -f "$ROOT/daemon.pid" ]]; then
    pid="$(cat "$ROOT/daemon.pid" || true)"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    fi
    rm -f "$ROOT/daemon.pid"
  fi
}

case "${1:-start}" in
stop) stop_all; exit 0 ;;
restart) stop_all ;;  # keep the store: used by the upgrade walkthrough
start)
  stop_all
  rm -rf "$ROOT"
  mkdir -p "$ROOT/data" "$ROOT/repos/acme" "$ROOT/logs"
  for name in app other; do
    git init -q --bare -b main "$ROOT/repos/acme/${name}.git"
    tmp="$(mktemp -d)"
    git -C "$tmp" init -q -b main
    echo "# acme/${name}" >"$tmp/README.md"
    git -C "$tmp" add README.md
    git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed"
    git -C "$tmp" push -q "$ROOT/repos/acme/${name}.git" main
    rm -rf "$tmp"
  done
  ;;
esac

nohup "$GRAIN_BIN" daemon \
  -data-dir "$ROOT/data" \
  -ui-addr "$UI_ADDR" \
  -sandbox-port-base=0 \
  "${SANDBOX_FLAGS[@]}" \
  -ci=false \
  -poll-interval 2s \
  -github-host "127.0.0.1:1" \
  -github-insecure-http \
  -as "operator@example.com" \
  -repo acme/app \
  -repo acme/other \
  "${EXTRA_FLAGS[@]}" \
  >>"$ROOT/logs/daemon.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 120); do
  curl -fsS "http://${UI_ADDR}/api/auth/access-code" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -fsS "http://${UI_ADDR}/api/auth/access-code" >/dev/null
ACCESS_CODE="$(tr -d '\r\n' <"$ROOT/data/local-api-token")"

if [[ "${1:-start}" == start ]]; then
  curl -fsS -X POST "http://${UI_ADDR}/api/deployment/git-connections" \
    -H "X-Grain-Access-Code: ${ACCESS_CODE}" -H "Content-Type: application/json" \
    -d '{"type":"pat","host":"127.0.0.1:1","value":"grain-walkthrough-stub"}' >/dev/null
  curl -fsS -X PUT "http://${UI_ADDR}/api/settings" \
    -H "X-Grain-Access-Code: ${ACCESS_CODE}" -H "Content-Type: application/json" \
    -d '{"externalAgentApi":true}' >/dev/null
fi

cat >"$ROOT/vars.env" <<VARS
export WALKTHROUGH_DIR="$ROOT"
export UI_URL="http://${UI_ADDR}"
export ACCESS_CODE="${ACCESS_CODE}"
export DAEMON_LOG="$ROOT/logs/daemon.log"
export STORE_DB="$ROOT/data/store/grain.db"
VARS
echo "Ready: UI_URL=http://${UI_ADDR} daemon_pid=$(cat "$ROOT/daemon.pid")"
