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

# 2026-11-09 §4 item 4 + CLI check:
# Reuses task 3731's `gcpstub.go` + `registry-fakegcp.go` with `SA_DELAY=60` to
# hold a real hosted instance (`walk-creating`) in `creating` with the live
# service-account propagation `status_detail`. While the entry is in that
# window, captures:
#   - `grain registry list` (text and -json)
#   - `grain registry show walk-creating` (text)
#   - `GET /api/entries/walk-creating`
#   - Playwright Chromium screenshots of `/` and `/entries/walk-creating` at
#     1440x900 and 390x844 in light and dark (via ui-registry/e2e/screenshot.mjs
#     through iapstub `-as alice@example.com`, producing contact-sheet.png).
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
HIC="$REPO/docs/test-runs/2026-11-09-hosted-instance-credentials"
W="${W:-/tmp/hic-3745}"
G="${G:-/home/debian/bin/grain}"
SA_DELAY="${SA_DELAY:-60}"
SHOTS_OUT="${SHOTS_OUT:-}"
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
STUB=http://127.0.0.1:4271
AUD=/projects/made-up/global/backendServices/registry

cleanup() {
  pkill -f "$W/" 2>/dev/null || true
  for d in "$W/pg-tls" "$W/pg-plain"; do
    [ -d "$d" ] && "$PGBIN/pg_ctl" -D "$d" -m immediate stop >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT
cleanup
pkill -x postgres 2>/dev/null || true
sleep 0.3
rm -rf "$W"
mkdir -p "$W/logs" "$W/keys"

start_pg() {
  local dir=$1 port=$2 ssl=$3
  "$PGBIN/initdb" -D "$dir" -U postgres --auth-local=trust --auth-host=scram-sha-256 --encoding=UTF8 --locale=C --no-instructions >/dev/null
  if [ "$ssl" = on ]; then
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
      -subj "/CN=grain-instance-postgres" -keyout "$dir/server.key" -out "$dir/server.crt" >/dev/null 2>&1
    chmod 0600 "$dir/server.key"
    printf 'local all all trust\nhostssl all all 127.0.0.1/32 scram-sha-256\n' >"$dir/pg_hba.conf"
  else
    printf 'local all all trust\nhost all all 127.0.0.1/32 scram-sha-256\n' >"$dir/pg_hba.conf"
  fi
  "$PGBIN/pg_ctl" -D "$dir" -l "$W/logs/pg-$port.log" -w start \
    -o "-p $port -k $W -c listen_addresses=127.0.0.1 -c ssl=$ssl -c password_encryption=scram-sha-256" >/dev/null
  psql -h "$W" -p "$port" -U postgres -qc "ALTER ROLE postgres PASSWORD 'walk-admin'" >/dev/null
}

start_pg "$W/pg-tls" 5432 on
start_pg "$W/pg-plain" 4272 off
psql -h "$W" -p 4272 -U postgres -qc "CREATE DATABASE registry" >/dev/null

openssl ecparam -name prime256v1 -genkey -noout -out "$W/keys/registry.pem"
(cd "$REPO" && go build -o "$W/iapstub" docs/test-runs/2026-11-07-grain-registry/iapstub.go)
(cd "$REPO" && go build -o "$W/gcpstub" "$HIC/gcpstub.go")
(cd "$REPO" && go build -o "$W/registry-fakegcp" "$HIC/registry-fakegcp.go")

nohup "$W/gcpstub" -addr 127.0.0.1:4271 -state "$W/stub" -vm-addr 127.0.0.1:8080 >>"$W/logs/gcpstub.log" 2>&1 &
nohup "$W/iapstub" -key "$W/keys/iap.pem" -jwks-addr 127.0.0.1:4280 >>"$W/logs/iap.log" 2>&1 &
sleep 0.5
nohup "$W/iapstub" -key "$W/keys/iap.pem" -addr 127.0.0.1:4281 -upstream http://127.0.0.1:4282 -audience "$AUD" -as alice@example.com >>"$W/logs/iap.log" 2>&1 &

cat >"$W/registry.yaml" <<EOF
listen: "127.0.0.1:4282"
jwks_listen: "127.0.0.1:4283"
host: registry.walk.test
instance_domain: grain-user.test
auth:
  provider: iap
  iap_audience: $AUD
  iap_certs_url: http://127.0.0.1:4280/jwks
  allowed_domains: [example.com]
  admins: [user:root@example.com]
groups:
  provider: static
signing:
  provider: local
  key_file: $W/keys/registry.pem
  key_id: walk-1
  jwt_ttl: 60s
database:
  url: postgres://postgres:walk-admin@127.0.0.1:4272/registry?sslmode=disable
compute:
  provider: gce
  project: made-up-proj
  region: us-central1
  zones: [us-central1-a]
  network: walk-net
  subnetwork: walk-subnet
  boot_image: projects/made-up-proj/global/images/grain-walk
  startup_script: "#!/bin/sh\ntrue\n"
  vm_prefix: grain-vm-
  sa_prefix: gi-
  bind_project_iam: true
  grant_self_act_as: false
  health_timeout: 2m
  status_poll_interval: 2s
  default_shape: n2-standard-2
  shapes:
    - name: n2-standard-2
      machine_type: n2-standard-2
      boot_disk_gb: 50
      boot_disk_type: pd-balanced
      quota_units: 1
instance_sql:
  cloud_sql_admin: false
  admin_dsn_template: postgres://postgres:walk-admin@127.0.0.1:5432/{database}?sslmode=require
  sql_instances:
    - name: walk-pool-01
      connection_name: made-up-proj:us-central1:walk-pool-01
      private_ip: 127.0.0.1
      max_databases: 10
secrets:
  project: made-up-proj
  secret_prefix: grain-inst-
config_repos:
  provider: none
EOF

"$W/registry-fakegcp" validate -config "$W/registry.yaml" || exit 1
nohup "$W/registry-fakegcp" serve -config "$W/registry.yaml" >>"$W/logs/registry.log" 2>&1 &
for _ in $(seq 1 50); do curl -fsS http://127.0.0.1:4283/healthz >/dev/null 2>&1 && break; sleep 0.2; done

b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
tok() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
reg() { GRAIN_REGISTRY_URL=http://127.0.0.1:4281 GRAIN_REGISTRY_TOKEN="$(tok alice@example.com)" "$G" "$@" 2>&1; }

curl -fsS -X POST "$STUB/_stub/sa-delay?seconds=$SA_DELAY" >/dev/null
echo "=== Creating walk-creating with SA_DELAY=${SA_DELAY}s ==="
reg registry create -shape n2-standard-2 -listed walk-creating | sed 's/^/  /'

# Wait until the reconciler records the service-account propagation status_detail.
for _ in $(seq 1 40); do
  detail=$(curl -sS -H "X-Test-Email: alice@example.com" http://127.0.0.1:4281/api/entries/walk-creating | jq -r '.status_detail // ""')
  if [ -n "$detail" ]; then
    break
  fi
  sleep 0.5
done

echo ""
echo "=== API GET /api/entries/walk-creating (while creating) ==="
curl -sS -H "X-Test-Email: alice@example.com" http://127.0.0.1:4281/api/entries/walk-creating |
  jq '{name, kind, state, status, status_detail, shape, boot_disk_gb}' | sed 's/^/  /'

echo ""
echo "=== grain registry list (while creating) ==="
reg registry list | sed 's/^/  /'

echo ""
echo "=== grain registry show walk-creating (while creating) ==="
reg registry show walk-creating | sed 's/^/  /'

if [ -n "$SHOTS_OUT" ]; then
  echo ""
  echo "=== Capturing Playwright screenshots of / and /entries/walk-creating -> $SHOTS_OUT ==="
  (
    cd "$REPO/ui-registry" &&
      PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
        node e2e/screenshot.mjs --url http://127.0.0.1:4281 \
        --pages "/,/entries/walk-creating" --full-page --out "$SHOTS_OUT"
  ) 2>&1 | sed 's/^/  /'
  if [ -f /tmp/check-creating-geom.mjs ]; then
    echo ""
    echo "=== Playwright geometry & overflow audit on / and /entries/walk-creating ==="
    PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
      node /tmp/check-creating-geom.mjs audit http://127.0.0.1:4281 2>&1 | sed 's/^/  /'
  fi
fi
