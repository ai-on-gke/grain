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

# Real-daemon walkthrough harness for docs/test-plans/2026-11-19.md §9:
# starts ghsim (127.0.0.1:4284), iapsim (issuer 127.0.0.1:4285, proxy
# 127.0.0.1:4286), and a real `grain daemon -sandbox-port-base=0`
# (127.0.0.1:4280) backed by PostgreSQL (STORE=postgres) or SQLite
# (STORE=sqlite) with operator alice@example.com (grain/operator) and
# viewer viewer@example.com (grain/viewer).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
WORK=${WORK_DIR:-/tmp/walk-s9/pg}
STORE=${STORE:-postgres}
DAEMON_PORT=${DAEMON_PORT:-4280}
GHSIM_PORT=${GHSIM_PORT:-4284}
IAP_ISSUER_PORT=${IAP_ISSUER_PORT:-4285}
IAP_PROXY_PORT=${IAP_PROXY_PORT:-4286}
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_s9?sslmode=disable}
AUD="/projects/123/global/backendServices/456"
ISS="https://iap.test.local"

stop_all() {
  for p in daemon ghsim iapsim demo; do
    if [[ -f "$WORK/$p.pid" ]]; then
      kill "$(cat "$WORK/$p.pid")" 2>/dev/null || true
      rm -f "$WORK/$p.pid"
    fi
  done
  sleep 0.5
}

stop_all
rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes" "$WORK/bin" "$WORK/home" "$WORK/runs" "$WORK/shots"

if [[ "$STORE" == "postgres" ]]; then
  sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_s9 WITH (FORCE);"
  sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_s9 OWNER grain;"
fi

(cd "$ROOT" && go build -o "$WORK/bin/ghsim" docs/test-runs/2026-11-13-grain-health-exit-codes/ghsim.go)
(cd "$ROOT" && go build -o "$WORK/bin/iapsim" tests/iap/iapsim.go)

git init -q --bare -b main "$WORK/widgets.git"
git -C "$WORK/widgets.git" config http.receivepack true
tmp=$(mktemp -d)
git -C "$tmp" init -q -b main
mkdir -p "$tmp/.grain/workflows" "$tmp/.github/workflows"
cat >"$tmp/README.md" <<'EOF'
# acme/widgets
EOF
cat >"$tmp/.grain/workflows/ci.yml" <<'EOF'
name: ci
on:
  push:
  pull_request:
jobs:
  unit:
    runs-on: linux
    capabilities: []
    steps:
      - name: unit
        run: echo PASS
EOF
cat >"$tmp/.github/workflows/ci.yml" <<'EOF'
name: CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo ok
EOF
git -C "$tmp" add .
git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed widgets"
git -C "$tmp" push -q "$WORK/widgets.git" main
rm -rf "$tmp"

echo "ghp_walkthrough" >"$WORK/bot.token"
echo "walkthrough-gemini-key" >"$WORK/gemini.key"
echo "GRAIN_IMAGE=localhost:5000/acme/grain:v1" >"$WORK/image.env"
printf '#!/bin/sh\ndate -u +%%T >>%s/restarted\n' "$WORK" >"$WORK/restart.sh"
chmod +x "$WORK/restart.sh"
echo '{"directConfigWrites": true}' >"$WORK/data/state-repo.json"

cat >"$WORK/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK/bot.token
EOF
cat >"$WORK/seed-models.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK/bot.token
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro]
    credential_file: $WORK/gemini.key
default_model: gemini-3.1-pro
EOF

if [[ "$STORE" == "sqlite" ]]; then
  "$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed-git.yaml" >"$WORK/connections.log" 2>&1
else
  "$GRAIN" connections -data-dir "$WORK/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK/seed-git.yaml" >"$WORK/connections.log" 2>&1
fi

"$WORK/bin/ghsim" -addr "127.0.0.1:${GHSIM_PORT}" -bare "$WORK/widgets.git" -default main >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"
for _ in $(seq 50); do
  curl -sf "http://127.0.0.1:${GHSIM_PORT}/repos/acme/widgets" >/dev/null && break
  sleep 0.1
done

"$WORK/bin/iapsim" \
  -issuer "127.0.0.1:${IAP_ISSUER_PORT}" \
  -proxy "127.0.0.1:${IAP_PROXY_PORT}" \
  -upstream "http://127.0.0.1:${DAEMON_PORT}" \
  -aud "$AUD" \
  -iss "$ISS" >"$WORK/iapsim.log" 2>&1 &
echo $! >"$WORK/iapsim.pid"
for _ in $(seq 50); do
  curl -sf "http://127.0.0.1:${IAP_ISSUER_PORT}/jwks" >/dev/null && break
  sleep 0.1
done

store_flags=()
if [[ "$STORE" == "postgres" ]]; then
  store_flags=(-postgres-url "$PG_DSN")
fi

chmod +x "$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py"

env -i \
  PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
  HOME="$WORK/home" \
  WALK_DIR="$WORK" \
  PYTHONDONTWRITEBYTECODE=1 \
  GCE_METADATA_HOST="127.0.0.1:9" \
  setsid "$GRAIN" daemon \
  -data-dir "$WORK/data" \
  -sandbox-dir "$WORK/sandboxes" \
  -host-sandboxes \
  -ui-addr "127.0.0.1:${DAEMON_PORT}" \
  -sandbox-port-base=0 \
  -ui-iap-audience "$AUD" \
  -ui-iap-issuer "$ISS" \
  -ui-iap-keys-url "http://127.0.0.1:${IAP_ISSUER_PORT}/jwks" \
  -deployment-owner alice@example.com \
  -as alice@example.com \
  -repo acme/widgets \
  -default-target-repo acme/widgets \
  -poll-interval 500ms \
  -max-workers 2 \
  -max-mergers 0 \
  -agent-framework antigravity \
  -agy-path "$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py" \
  -gemini-api-key-file "$WORK/gemini.key" \
  -github-host "127.0.0.1:${GHSIM_PORT}" \
  -github-insecure-http \
  -upgrade-image "localhost:5000/acme/grain" \
  -upgrade-image-ref-file "$WORK/image.env" \
  -upgrade-allowed-branches '**' \
  -upgrade-grace-window 30s \
  -upgrade-restart-cmd "$WORK/restart.sh" \
  "${store_flags[@]}" >>"$WORK/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK/daemon.pid"

CODE=""
for _ in $(seq 120); do
  if [[ -s "$WORK/data/local-api-token" ]]; then
    CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
    if curl -sf -H "Authorization: Bearer $CODE" "http://127.0.0.1:${DAEMON_PORT}/api/config" >/dev/null 2>&1; then
      break
    fi
  fi
  if ! kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null; then
    echo "daemon exited early:" >&2
    tail -n 60 "$WORK/daemon.log" >&2
    exit 1
  fi
  sleep 0.2
done

if [[ "$STORE" == "sqlite" ]]; then
  "$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed-models.yaml" >>"$WORK/connections.log" 2>&1
else
  "$GRAIN" connections -data-dir "$WORK/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK/seed-models.yaml" >>"$WORK/connections.log" 2>&1
fi
sleep 0.6

curl -sf -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
  -d '{"entry":"user:alice@example.com","role":"grain/operator","reason":"walkthrough operator"}' \
  "http://127.0.0.1:${DAEMON_PORT}/api/access/principals" >/dev/null
curl -sf -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
  -d '{"entry":"user:viewer@example.com","role":"grain/viewer","reason":"walkthrough viewer"}' \
  "http://127.0.0.1:${DAEMON_PORT}/api/access/principals" >/dev/null

echo "=== Operator /api/config (via IAP proxy) ==="
curl -sf -H "X-Iapsim-Email: alice@example.com" "http://127.0.0.1:${IAP_PROXY_PORT}/api/config" | jq -c '{role:.access.role,full:.access.full}'
echo "=== Viewer /api/config (via IAP proxy) ==="
curl -sf -H "X-Iapsim-Email: viewer@example.com" "http://127.0.0.1:${IAP_PROXY_PORT}/api/config" | jq -c '{role:.access.role,full:.access.full}'
echo "READY: DAEMON=http://127.0.0.1:${DAEMON_PORT} PROXY=http://127.0.0.1:${IAP_PROXY_PORT} STORE=${STORE}"
