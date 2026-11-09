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

# Stand up the 2026-11-08 §1 (Registry Phase 2) walkthrough environment: a
# throwaway PostgreSQL cluster, the IAP/JWKS stub from the 2026-11-07 walk and
# two real `grain-registry serve` replicas sharing one database and one
# signing key, with `delegation_policy: consent`.
#
#   WALK=/tmp/gr2 docs/test-runs/2026-11-08-grain-registry-phase2/env-up.sh
#   SCOPE_OWNERS=1 RESTART=1 .../env-up.sh   # restart both replicas with catalog.scope_owners
#   ONLY=a RESTART=1 .../env-up.sh           # restart only replica A
#
# Ports (42xx convention):
#   4240 postgres          4250 IAP JWKS
#   4251 IAP -> replica A main 4252 (public 4253, peer 4254)
#   4261 IAP -> replica B main 4262 (public 4263, peer 4264)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
WALK="${WALK:-/tmp/gr2}"
BIN="${BIN:-/home/debian/bin}"
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
mkdir -p "$WALK/logs" "$WALK/keys"

# 1. PostgreSQL in $WALK/pg (trust auth, loopback only).
if [ ! -d "$WALK/pg" ]; then
  "$PGBIN/initdb" -D "$WALK/pg" -U postgres --auth=trust --no-instructions --encoding=UTF8 --locale=C >"$WALK/logs/initdb.log"
fi
if ! "$PGBIN/pg_ctl" -D "$WALK/pg" status >/dev/null 2>&1; then
  "$PGBIN/pg_ctl" -D "$WALK/pg" -l "$WALK/logs/postgres.log" -o "-p 4240 -k $WALK -c listen_addresses=127.0.0.1" -w start >/dev/null
fi
psql -h 127.0.0.1 -p 4240 -U postgres -tAc "SELECT 1 FROM pg_database WHERE datname='registry'" | grep -q 1 ||
  psql -h 127.0.0.1 -p 4240 -U postgres -c "CREATE DATABASE registry" >/dev/null

# 2. Shared registry signing key (both replicas mint verifiable assertions).
[ -f "$WALK/keys/registry.pem" ] || openssl ecparam -name prime256v1 -genkey -noout -out "$WALK/keys/registry.pem"

# 3. IAP stub: JWKS + one identity-injecting front proxy per replica.
AUD=/projects/walk/global/backendServices/registry
[ -x "$WALK/iapstub" ] || (cd "$REPO" && go build -o "$WALK/iapstub" "$REPO/docs/test-runs/2026-11-07-grain-registry/iapstub.go")
start() { # name cmd...
  local name=$1
  shift
  if [ -f "$WALK/$name.pid" ] && kill -0 "$(cat "$WALK/$name.pid")" 2>/dev/null; then return; fi
  nohup "$@" >>"$WALK/logs/$name.log" 2>&1 &
  echo $! >"$WALK/$name.pid"
}
stop() { # name
  local pid
  [ -f "$WALK/$1.pid" ] || return 0
  pid="$(cat "$WALK/$1.pid")"
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 60); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
  rm -f "$WALK/$1.pid"
}
start iap-jwks "$WALK/iapstub" -key "$WALK/keys/iap.pem" -jwks-addr 127.0.0.1:4250
sleep 0.5
start iap-a "$WALK/iapstub" -key "$WALK/keys/iap.pem" -addr 127.0.0.1:4251 -upstream http://127.0.0.1:4252 -audience $AUD
start iap-b "$WALK/iapstub" -key "$WALK/keys/iap.pem" -addr 127.0.0.1:4261 -upstream http://127.0.0.1:4262 -audience $AUD

# 4. Registry configs (rewritten every run so SCOPE_OWNERS toggles take effect).
scope_owners=""
if [ "${SCOPE_OWNERS:-0}" = 1 ]; then
  scope_owners='  scope_owners:
    - repo: "acme/*"
      principals: [group:acme-owners@example.com]
    - repo: "gitlab.example.com/acme/*"
      principals: [user:alt@example.com]'
fi
for r in ${ONLY:-a b}; do
  if [ "$r" = a ]; then base=4252; else base=4262; fi
  cat >"$WALK/registry-$r.yaml" <<EOF
listen: "127.0.0.1:$base"
jwks_listen: "127.0.0.1:$((base + 1))"
host: registry.walk.test
instance_domain: grain-user.test
auth:
  provider: iap
  iap_audience: $AUD
  iap_certs_url: http://127.0.0.1:4250/jwks
  allowed_domains: [example.com]
  admins: [user:root@example.com]
  service_principals:
    - serviceAccount:ci-bot@walk-proj.iam.gserviceaccount.com
    - serviceAccount:other-bot@walk-proj.iam.gserviceaccount.com
  delegation_policy: ${DELEGATION_POLICY:-consent}
groups:
  provider: static
  static_members:
    acme-owners@example.com: [carol@example.com]
    eng@example.com: [alice@example.com, bob@example.com]
signing:
  provider: local
  key_file: $WALK/keys/registry.pem
  key_id: walk-1
  jwt_ttl: 60s
catalog:
  heartbeat_interval: 2s
  degraded_after: 6s
  offline_after: 12s
  cache_ttl: 1s
$scope_owners
tunnel:
  transport: ws-mux
  peer_listen: "127.0.0.1:$((base + 2))"
  replica_heartbeat: 1s
  replica_ttl: 4s
  reconnect_grace: ${GRACE:-5s}
  drain_timeout: ${DRAIN:-5s}
  max_concurrent_streams: 64
  max_buffered_bytes: 4194304
database:
  url: postgres://postgres@127.0.0.1:4240/registry?sslmode=disable
compute:
  provider: none
EOF
  "$BIN/grain-registry" validate -config "$WALK/registry-$r.yaml" >/dev/null
  if [ "${RESTART:-0}" = 1 ]; then stop "registry-$r"; fi
  start "registry-$r" "$BIN/grain-registry" serve -config "$WALK/registry-$r.yaml"
done
for p in 4252 4262; do
  for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$((p + 1))/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
done
echo "environment up under $WALK (scope_owners=${SCOPE_OWNERS:-0})"
