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

# §4 item 2 (task 3731): a real grain-registry (registry-fakegcp.go, the
# real gce providers pointed at gcpstub.go) on PostgreSQL provisions a hosted
# instance while the stub keeps its new service account invisible to Compute
# for SA_DELAY seconds (-1 = forever). instance-init-walk.sh must have run
# (TLS pool on 5432, plain PostgreSQL on 4272).
#
#   SA_DELAY=20 ./provision-walk.sh walk-late
#   SA_DELAY=-1 ./provision-walk.sh walk-never
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
W="${W:-/tmp/hic}"
G="${G:-/home/debian/bin/grain}"
NAME="${1:-walk-late}"
SA_DELAY="${SA_DELAY:-20}"
STUB=http://127.0.0.1:4271
AUD=/projects/made-up/global/backendServices/registry
mkdir -p "$W/logs" "$W/keys"

for port in 5432 4272; do
  psql -h "$W" -p $port -U postgres -qc "ALTER ROLE postgres PASSWORD 'walk-admin'" >/dev/null
done
# Each walk starts from an empty registry store (the per-owner quota is 2 units).
pkill -f "^$W/registry-fakegcp" 2>/dev/null; sleep 0.5
psql -h "$W" -p 4272 -U postgres -qc "DROP DATABASE IF EXISTS registry" >/dev/null
psql -h "$W" -p 4272 -U postgres -qc "CREATE DATABASE registry" >/dev/null
[ -f "$W/keys/registry.pem" ] || openssl ecparam -name prime256v1 -genkey -noout -out "$W/keys/registry.pem"
[ -x "$W/iapstub" ] || (cd "$REPO" && go build -o "$W/iapstub" docs/test-runs/2026-11-07-grain-registry/iapstub.go)
[ -x "$W/registry-fakegcp" ] || (cd "$REPO" && go build -o "$W/registry-fakegcp" "$HERE/registry-fakegcp.go")
[ -x "$W/gcpstub" ] || (cd "$REPO" && go build -o "$W/gcpstub" "$HERE/gcpstub.go")

bg() { local name=$1; shift; pkill -f "^$W/$name" 2>/dev/null; sleep 0.3; nohup "$W/$name" "$@" >>"$W/logs/$name.log" 2>&1 & }
pkill -f "^$W/gcpstub" 2>/dev/null; sleep 0.3
nohup "$W/gcpstub" -addr 127.0.0.1:4271 -state "$W/stub" -vm-addr 127.0.0.1:8080 >>"$W/logs/gcpstub.log" 2>&1 &
pkill -f "^$W/iapstub" 2>/dev/null; sleep 0.3
nohup "$W/iapstub" -key "$W/keys/iap.pem" -jwks-addr 127.0.0.1:4280 >>"$W/logs/iap.log" 2>&1 &
sleep 0.5
nohup "$W/iapstub" -key "$W/keys/iap.pem" -addr 127.0.0.1:4281 -upstream http://127.0.0.1:4282 -audience $AUD >>"$W/logs/iap.log" 2>&1 &

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
bg registry-fakegcp serve -config "$W/registry.yaml"
for _ in $(seq 1 50); do curl -fsS http://127.0.0.1:4283/healthz >/dev/null 2>&1 && break; sleep 0.2; done

b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
tok() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
reg() { GRAIN_REGISTRY_URL=http://127.0.0.1:4281 GRAIN_REGISTRY_TOKEN="$(tok alice@example.com)" "$G" registry "$@" 2>&1; }

curl -fsS -X POST "$STUB/_stub/sa-delay?seconds=$SA_DELAY" >/dev/null
echo "### SA_DELAY=$SA_DELAY: grain registry create $NAME"
start=$(date +%s)
reg create -shape n2-standard-2 "$NAME" | sed 's/^/  /'
last=""
for _ in $(seq 1 100); do
  line="$(curl -sS -H "X-Test-Email: alice@example.com" http://127.0.0.1:4281/api/entries/$NAME | jq -r "\"state=\\(.state) status_detail=\\(.status_detail // \"\")\"")"
  if [ "$line" != "$last" ]; then echo "  t+$(($(date +%s) - start))s: $line"; last="$line"; fi
  case "$line" in state=running*|state=failed*) break ;; esac
  sleep 2
done
echo "### grain registry show $NAME"
reg show "$NAME" | sed 's/^/  /'
echo "### stub: Compute inserts, Secret Manager versions, SA creations"
grep -c "POST /compute/v1/projects/made-up-proj/zones/us-central1-a/instances " "$W/stub/requests.log" | sed 's/^/  instance inserts (all walks so far): /'
curl -fsS "$STUB/_stub/secrets" | jq -r --arg n "$NAME" 'to_entries[] | select(.key | contains($n)) | "  \(.key): \(.value) version(s)"'
echo "### tenant role on the TLS pool"
psql -h "$W" -p 5432 -U postgres -tAc "SELECT rolname, rolcanlogin FROM pg_roles WHERE rolname LIKE 'gi-${NAME}%'" | sed 's/^/  /'
