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

# 2026-11-10 §7 item 4 (and the environment item 3's UI walk runs over):
# a HEAD hub (hub-fakegcp.go: the real gce compute + resources providers and
# reconciler against gcpstub.go) on PostgreSQL, with compute.hub_id,
# grant_self_act_as: true and a fixed self_principal.
#
#   fakegcp-walk.sh           # E2 refusals, actAs 409/404/race/existing-binding, hub_id labels
#   KEEP=1 fakegcp-walk.sh    # ... and leave everything running for ui-walk.mjs
#
# Ports: gcpstub 4271, IAP JWKS 4280, IAP (as alice) 4281 / (as root) 4284,
# hub 4282 (jwks 4283), hub PostgreSQL 4272, instance PostgreSQL (TLS) 4241.
# The gce provider probes a hosted VM on networkIP:8080 (gcp.InstanceHTTPPort),
# so gcpstub's healthy fake VM listens there.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
W="${W:-/tmp/fakegcp-walk}"
G="${G:-/tmp/bin-new/grain}"
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
STUB=http://127.0.0.1:4271
AUD=/projects/made-up-proj/global/backendServices/hub
SELF=serviceAccount:hub-walk@made-up-proj.iam.gserviceaccount.com

cleanup() {
  pkill -f "$W/" 2>/dev/null || true
  for d in "$W/pg-tls" "$W/pg-plain"; do
    [ -d "$d" ] && "$PGBIN/pg_ctl" -D "$d" -m immediate stop >/dev/null 2>&1 || true
  done
}
[ "${KEEP:-0}" = 1 ] || trap cleanup EXIT
cleanup
rm -rf "$W"
mkdir -p "$W/logs" "$W/keys" "$W/bin"

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
start_pg "$W/pg-tls" 4241 on
start_pg "$W/pg-plain" 4272 off
psql -h "$W" -p 4272 -U postgres -qc "CREATE DATABASE hub" >/dev/null

openssl ecparam -name prime256v1 -genkey -noout -out "$W/keys/hub.pem"
(cd "$REPO" && go build -o "$W/bin/iapstub" docs/test-runs/2026-11-07-grain-registry/iapstub.go &&
  go build -o "$W/bin/gcpstub" "$HERE/gcpstub.go" && go build -o "$W/bin/hub-fakegcp" "$HERE/hub-fakegcp.go") || exit 1

nohup "$W/bin/gcpstub" -addr 127.0.0.1:4271 -state "$W/stub" -vm-addr 127.0.0.1:8080 >>"$W/logs/gcpstub.log" 2>&1 &
nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -jwks-addr 127.0.0.1:4280 >>"$W/logs/iap.log" 2>&1 &
sleep 0.5
nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -addr 127.0.0.1:4281 -upstream http://127.0.0.1:4282 -audience "$AUD" -as alice@example.com >>"$W/logs/iap.log" 2>&1 &
nohup "$W/bin/iapstub" -key "$W/keys/iap.pem" -addr 127.0.0.1:4284 -upstream http://127.0.0.1:4282 -audience "$AUD" -as root@example.com >>"$W/logs/iap.log" 2>&1 &

write_config() { # write_config <file> <shapes yaml>
  cat >"$1" <<EOF
listen: "127.0.0.1:4282"
jwks_listen: "127.0.0.1:4283"
host: hub.walk.test
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
  key_file: $W/keys/hub.pem
  key_id: walk-1
  jwt_ttl: 60s
database:
  url: postgres://postgres:walk-admin@127.0.0.1:4272/hub?sslmode=disable
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
  hub_id: walk-hub-7
  bind_project_iam: true
  grant_self_act_as: true
  self_principal: $SELF
  health_timeout: 2m
  status_poll_interval: 2s
  default_shape: n2-standard-2
  shapes:
$2
instance_sql:
  cloud_sql_admin: false
  admin_dsn_template: postgres://postgres:walk-admin@127.0.0.1:4241/{database}?sslmode=require
  sql_instances:
    - name: walk-pool-01
      connection_name: made-up-proj:us-central1:walk-pool-01
      private_ip: 127.0.0.1
      max_databases: 20
secrets:
  project: made-up-proj
  secret_prefix: grain-inst-
config_repos:
  provider: none
quotas:
  default_instance_units: 40
EOF
}
N2='    - name: n2-standard-2
      machine_type: n2-standard-2
      boot_disk_gb: 50
      boot_disk_type: pd-balanced
      quota_units: 1
    - name: n2-standard-4
      machine_type: n2-standard-4
      boot_disk_gb: 100
      boot_disk_type: pd-balanced
      quota_units: 2'
E2="$N2
    - name: cheap
      machine_type: e2-medium
      boot_disk_gb: 50
      quota_units: 1"
E2Z="$N2
    - name: cheap-zonal
      machine_type: zones/us-central1-a/machineTypes/e2-small
      boot_disk_gb: 50
      quota_units: 1"

echo "=== item 4a: E2 shapes in configuration ==="
write_config "$W/hub-e2.yaml" "$E2"
write_config "$W/hub-e2z.yaml" "$E2Z"
for f in hub-e2.yaml hub-e2z.yaml; do
  "$W/bin/hub-fakegcp" validate -config "$W/$f" >"$W/out" 2>&1
  echo "  grain-hub validate $f -> exit $? : $(tr '\n' ' ' <"$W/out")"
  timeout 20 "$W/bin/hub-fakegcp" serve -config "$W/$f" >"$W/out" 2>&1
  echo "  grain-hub serve $f    -> exit $? : $(grep -v '^\s*$' "$W/out" | tail -n 1)"
done

write_config "$W/hub.yaml" "$N2"
"$W/bin/hub-fakegcp" validate -config "$W/hub.yaml" 2>&1 | sed 's/^/  N2 config: /'
nohup "$W/bin/hub-fakegcp" serve -config "$W/hub.yaml" >>"$W/logs/hub.log" 2>&1 &
for _ in $(seq 1 60); do curl -fsS http://127.0.0.1:4283/healthz >/dev/null 2>&1 && break; sleep 0.25; done
curl -fsS http://127.0.0.1:4283/healthz >/dev/null 2>&1 || { echo "hub did not start:"; tail -n 5 "$W/logs/hub.log"; exit 1; }
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
tok() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
hub() { GRAIN_HUB_URL=http://127.0.0.1:4281 GRAIN_HUB_TOKEN="$(tok alice@example.com)" "$G" hub "$@" 2>&1; }
api() { curl -sS -H 'X-Test-Email: alice@example.com' "http://127.0.0.1:4281$1"; }
wait_state() { # wait_state <entry> <state...>
  local e=$1 s
  shift
  for _ in $(seq 1 ${WAIT_TICKS:-120}); do
    s=$(api "/api/entries/$e" | jq -r '.status')
    for want in "$@"; do [ "$s" = "$want" ] && return 0; done
    sleep 0.5
  done
  return 1
}
iamlog() { # iamlog <sa email>: the stub's IAM-policy calls on one account
  curl -sS "$STUB/_stub/log" | grep -E "serviceAccounts/$1:(get|set)IamPolicy|concurrent writer added .* to $1" | sed -E 's/^[0-9TZ:-]+ //; s#/iam/v1/projects/made-up-proj/serviceAccounts/##; s/^/    /'
}
policy() { curl -sS "$STUB/_stub/sa-policy?email=$1" | jq -c '{etag, bindings}'; }
sa() { echo "gi-$1@made-up-proj.iam.gserviceaccount.com"; }

echo
echo "=== item 4b: API requests naming an E2 machine type ==="
api /api/shapes | head -c 400 | sed 's/^/  GET \/api\/shapes: /'; echo
for body in '{"name":"e2-api","kind":"instance","shape":"cheap"}' \
  '{"name":"e2-api","kind":"instance","machine_type":"zones/us-central1-a/machineTypes/e2-small"}' \
  '{"name":"e2-api","kind":"instance","shape":"n2-standard-2","machine_type":"zones/us-central1-a/machineTypes/e2-small"}'; do
  code=$(curl -sS -o "$W/out" -w '%{http_code}' -X POST -H 'X-Test-Email: alice@example.com' -H 'Content-Type: application/json' -d "$body" http://127.0.0.1:4281/api/entries)
  echo "  POST /api/entries $body -> HTTP $code $(head -c 300 "$W/out")"
  [ "$code" -lt 300 ] && { sleep 3; api /api/entries/e2-api | jq -c '{status, machine_type, status_detail}' | sed 's/^/      /'; curl -sS -X DELETE -H 'X-Test-Email: root@example.com' "http://127.0.0.1:4284/api/admin/entries/e2-api" >/dev/null; }
done
echo "  a shape row an older config seeded (zonal e2-small), inserted after migration 8:"
psql -h "$W" -p 4272 -U postgres hub -qAt -c "SELECT string_agg(column_name, ',') FROM information_schema.columns WHERE table_schema='registry' AND table_name='shapes'" | sed 's/^/    columns: /'
psql -h "$W" -p 4272 -U postgres hub -qAt -c "INSERT INTO registry.shapes (name, machine_type, boot_disk_gb, quota_units) VALUES ('legacy-e2', 'zones/us-central1-a/machineTypes/e2-small', 50, 1)" 2>&1 | sed 's/^/    /'
hub create -shape legacy-e2 legacy-e2-vm | sed 's/^/    /'
wait_state legacy-e2-vm failed running
api /api/entries/legacy-e2-vm | jq -c '{status, shape, status_detail}' | sed 's/^/    /'

echo
echo "=== item 4c: per-instance actAs (self_principal $SELF) ==="
echo "--- baseline: create act-plain"
hub create -shape n2-standard-2 act-plain | sed 's/^/    /'
wait_state act-plain running failed
api /api/entries/act-plain | jq -c '{status, status_detail}' | sed 's/^/    /'
iamlog "$(sa act-plain)"
echo "    policy: $(policy "$(sa act-plain)")"
curl -sS "$STUB/compute/v1/projects/made-up-proj/zones/us-central1-a/instances/grain-vm-act-plain" | jq -c '{labels, serviceAccounts: [.serviceAccounts[]?.email]}' | sed 's/^/    instance: /'

echo "--- inject 409, 409 on setIamPolicy: create act-409"
curl -sS -X POST "$STUB/_stub/iam-fail?op=set&codes=409,409" >/dev/null
hub create -shape n2-standard-2 act-409 | sed 's/^/    /'
wait_state act-409 running failed
api /api/entries/act-409 | jq -c '{status, status_detail}' | sed 's/^/    /'
iamlog "$(sa act-409)"
echo "    policy: $(policy "$(sa act-409)")"

echo "--- a real concurrent writer (etag moves between the hub's read and write): create act-race"
curl -sS -X POST "$STUB/_stub/iam-race?email=$(sa act-race)" -d '{"role":"roles/iam.serviceAccountTokenCreator","members":["user:ops@example.com"]}' >/dev/null
hub create -shape n2-standard-2 act-race | sed 's/^/    /'
wait_state act-race running failed
api /api/entries/act-race | jq -c '{status, status_detail}' | sed 's/^/    /'
iamlog "$(sa act-race)"
echo "    policy: $(policy "$(sa act-race)")"

echo "--- inject 404, 404 on getIamPolicy (account not yet visible to IAM): create act-404"
curl -sS -X POST "$STUB/_stub/iam-fail?op=get&codes=404,404" >/dev/null
hub create -shape n2-standard-2 act-404 | sed 's/^/    /'
wait_state act-404 running failed
api /api/entries/act-404 | jq -c '{status, status_detail}' | sed 's/^/    /'
iamlog "$(sa act-404)"
echo "    policy: $(policy "$(sa act-404)")"

echo "--- an existing unconditional binding (plus a conditional one) is left untouched: create act-existing"
curl -sS -X POST "$STUB/_stub/sa-policy?email=$(sa act-existing)" -d "{\"version\":3,\"bindings\":[{\"role\":\"roles/iam.serviceAccountUser\",\"members\":[\"user:ops@example.com\",\"$SELF\"]},{\"role\":\"roles/iam.serviceAccountUser\",\"members\":[\"user:temp@example.com\"],\"condition\":{\"title\":\"until-friday\",\"expression\":\"request.time < timestamp('2026-11-14T00:00:00Z')\"}}]}" >/dev/null
before=$(policy "$(sa act-existing)")
hub create -shape n2-standard-2 act-existing | sed 's/^/    /'
wait_state act-existing running failed
api /api/entries/act-existing | jq -c '{status, status_detail}' | sed 's/^/    /'
iamlog "$(sa act-existing)"
after=$(policy "$(sa act-existing)")
echo "    before: $before"
echo "    after:  $after"
[ "$before" = "$after" ] && echo "    -> policy byte-for-byte unchanged (no setIamPolicy)" || echo "    -> POLICY CHANGED"

echo "--- 409 on every write (exhausts the retries): create act-stuck"
curl -sS -X POST "$STUB/_stub/iam-fail?op=set&codes=409,409,409,409,409,409,409,409,409,409,409,409" >/dev/null
hub create -shape n2-standard-2 act-stuck | sed 's/^/    /'
for _ in $(seq 1 120); do
  d=$(api /api/entries/act-stuck | jq -r '.status_detail // ""')
  case "$d" in *"etag conflicts"*) break ;; esac
  sleep 0.5
done
api /api/entries/act-stuck | jq -c '{status, status_detail}' | sed 's/^/    /'
curl -sS -X POST "$STUB/_stub/iam-fail?op=set&codes=" >/dev/null
echo "    no access token in any logged URL: $(curl -sS "$STUB/_stub/log" | grep -c -i -E 'access_token|ya29') hits; tokeninfo calls: $(curl -sS "$STUB/_stub/log" | grep -c tokeninfo)"

echo
echo "=== CLI view (grain hub list / show) ==="
hub list | sed 's/^/  /'
hub show act-stuck | sed 's/^/  /'

if [ "${KEEP:-0}" = 1 ]; then
  echo
  echo "=== leaving the environment up for ui-walk.mjs (SA_DELAY=${SA_DELAY:-60}s creating entry: walk-creating) ==="
  curl -sS -X POST "$STUB/_stub/sa-delay?seconds=${SA_DELAY:-60}" >/dev/null
  hub create -shape n2-standard-2 -listed walk-creating | sed 's/^/  /'
fi
