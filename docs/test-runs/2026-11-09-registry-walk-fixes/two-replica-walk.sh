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

# 2026-11-09 §4 item 3 (two-replica CLI & API walk fixes):
# Reuses task 3636's two-replica PostgreSQL + IAP harness in
# docs/test-runs/2026-11-08-grain-registry-phase2/ (`env-up.sh`, `lib.sh`,
# `start-daemon.sh`) and verifies on BOTH replicas (A = :4251, B = :4261):
#   1. `grain registry resize -disk-gb 200` (and `upgrade`) on a connected
#      entry refuses `invalid_entry_kind` with no state or tunnel disruption;
#   2. `grain registry transfer -to bob x` then `-cancel x` withdraws the offer
#      across replicas, bob's `accept-transfer` fails clearly on both replicas,
#      and `-cancel` combined with `-to` or `-owner-group` is refused locally;
#   3. another person's deleted tombstone reads as 404 on both replicas, and
#      after an admin purge with `release_reservation: true` another user's
#      `grain registry register <name>` succeeds across both replicas;
#   4. `grain access list` on the connected daemon prints `label=<name>` for a
#      `delegate:` allowlist entry.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
PHASE2="$REPO/docs/test-runs/2026-11-08-grain-registry-phase2"
export WALK="${WALK:-/tmp/gr-walk-3745}"
export BIN="${BIN:-/home/debian/bin}"
export G="${G:-$BIN/grain}"

# Clean slate for a reproducible run.
pkill -f "$WALK/" 2>/dev/null || true
if [ -d "$WALK/pg" ]; then
  PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
  "$PGBIN/pg_ctl" -D "$WALK/pg" -m immediate stop >/dev/null 2>&1 || true
fi
rm -rf "$WALK"

"$PHASE2/env-up.sh"
"$PHASE2/start-daemon.sh"
# shellcheck source=../2026-11-08-grain-registry-phase2/lib.sh
. "$PHASE2/lib.sh"

sql() {
  echo "  sql> $1"
  "${PSQL[@]}" -c "$1" | sed 's/^/    /'
}

tunnel_whoami() {
  local reg=$1 entry=$2 who=$3
  local code
  code=$(curl -sS -o /tmp/gr-whoami.out -w '%{http_code}' \
    -H "Host: $entry.grain-user.test" -H "X-Test-Email: $who" "$reg/api/whoami")
  printf '%s %s\n' "$code" "$(head -c 160 /tmp/gr-whoami.out | tr '\n' ' ')"
}

"${PSQL[@]}" -c "insert into registry.shapes(name,machine_type,boot_disk_gb,description) values ('standard','n2-standard-4',50,'walk shape') on conflict do nothing" >/dev/null
# Wait briefly for conn-alice tunnel to settle.
for _ in $(seq 1 30); do
  code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Host: conn-alice.grain-user.test" -H "X-Test-Email: alice@example.com" "$RA/api/whoami")
  [ "$code" = 200 ] && break
  sleep 0.25
done

echo ""
echo "=== 1. Connected entry resize & upgrade refusals on both replicas ==="
echo "  tunnel before (via A): $(tunnel_whoami "$RA" conn-alice alice@example.com)"
echo "  tunnel before (via B): $(tunnel_whoami "$RB" conn-alice alice@example.com)"
sql "select name, kind, lifecycle_state, boot_disk_gb, coalesce(replace_step,''), coalesce(target_version,'') from registry.entries where name='conn-alice'"

as alice@example.com "$RA" resize -disk-gb 200 conn-alice
as alice@example.com "$RB" resize -disk-gb 200 conn-alice
api alice@example.com "$RA" POST /api/entries/conn-alice/replace '{"shape":"standard","boot_disk_gb":200}'
api alice@example.com "$RB" POST /api/entries/conn-alice/replace '{"shape":"standard","boot_disk_gb":200}'
as alice@example.com "$RA" upgrade -version v3.2.0 conn-alice
as alice@example.com "$RB" upgrade -version v3.2.0 conn-alice

sql "select name, kind, lifecycle_state, boot_disk_gb, coalesce(replace_step,''), coalesce(target_version,'') from registry.entries where name='conn-alice'"
echo "  tunnel after (via A): $(tunnel_whoami "$RA" conn-alice alice@example.com)"
echo "  tunnel after (via B): $(tunnel_whoami "$RB" conn-alice alice@example.com)"

echo ""
echo "=== 2. Two-step transfer offer, -cancel, accept-transfer & flag conflicts across both replicas ==="
echo "\$ [alice @ 4251] grain registry register -standalone xfer-walk"
"$G" registry register -standalone -registry-url "$RA" -registry-token "$(fakejwt alice@example.com)" \
  -mint-token -token-file "$WALK/xfer-walk.token" xfer-walk 2>&1 | sed 's/^/    /'

echo "--- offer on replica A, inspect on replica B, cancel on replica B"
as alice@example.com "$RA" transfer -to bob@example.com xfer-walk
as alice@example.com "$RB" show xfer-walk
as alice@example.com "$RB" transfer -cancel xfer-walk
as alice@example.com "$RA" show xfer-walk
as bob@example.com "$RA" accept-transfer xfer-walk
as bob@example.com "$RB" accept-transfer xfer-walk

echo "--- offer on replica B, cancel on replica A, bob accept-transfer on B"
as alice@example.com "$RB" transfer -to bob@example.com xfer-walk
as alice@example.com "$RA" transfer -cancel xfer-walk
as bob@example.com "$RB" accept-transfer xfer-walk
sql "select name, owner_email, coalesce(pending_owner,''), transfer_expires_at is not null as has_expiry from registry.entries where name='xfer-walk'"

echo "--- -cancel combined with -to or -owner-group refused locally"
as alice@example.com "$RA" transfer -cancel -to bob@example.com xfer-walk
as alice@example.com "$RB" transfer -cancel -owner-group group:eng@example.com xfer-walk

echo ""
echo "=== 3. Tombstone 404 & re-registration after admin purge across both replicas ==="
reg_standalone() {
  local who=$1 reg=$2 name=$3
  echo "\$ [$who @ ${reg##*:}] grain registry register -standalone $name"
  "$G" registry register -standalone -registry-url "$reg" -registry-token "$(fakejwt "$who")" \
    -mint-token -token-file "$WALK/${name}-${who%@*}.token" "$name" 2>&1 | sed 's/^/    /'
  echo "    (exit ${PIPESTATUS[0]})"
}

reg_standalone alice@example.com "$RA" reclaim-a
as alice@example.com "$RB" delete -yes reclaim-a
echo "--- tombstone visibility before purge (bob gets 404 on A and B; owner alice gets 200)"
api bob@example.com "$RA" GET /api/entries/reclaim-a
api bob@example.com "$RB" GET /api/entries/reclaim-a
api alice@example.com "$RA" GET /api/entries/reclaim-a
echo "--- before reservation release, bob gets name_reserved (not 403 forbidden)"
reg_standalone bob@example.com "$RB" reclaim-a

echo "--- admin purge on replica A with release_reservation=true, bob registers on replica B"
api root@example.com "$RA" POST /api/admin/entries/reclaim-a/purge '{"release_reservation":true}'
api bob@example.com "$RA" GET /api/entries/reclaim-a
api bob@example.com "$RB" GET /api/entries/reclaim-a
api bob@example.com "$RA" GET /api/names/reclaim-a
api bob@example.com "$RB" GET /api/names/reclaim-a
reg_standalone bob@example.com "$RB" reclaim-a
as bob@example.com "$RA" show reclaim-a

echo "--- reverse direction: carol registers on B, deletes on A, admin purges on B, bob registers on A"
reg_standalone carol@example.com "$RB" reclaim-b
as carol@example.com "$RA" delete -yes reclaim-b
api root@example.com "$RB" POST /api/admin/entries/reclaim-b/purge '{"release_reservation":true}'
reg_standalone bob@example.com "$RA" reclaim-b
as bob@example.com "$RB" show reclaim-b

echo ""
echo "=== 4. grain access list prints label= for a delegate on the connected daemon ==="
(
  export GRAIN_SERVER=http://127.0.0.1:4270 GRAIN_DATA_DIR="$WALK/d1"
  echo "\$ grain access allow --label ci-bot --role grain/delegated --reason 'walk delegated ci-bot' delegate:$SA"
  "$G" access allow --label ci-bot --role grain/delegated --reason "walk delegated ci-bot" "delegate:$SA" 2>&1 | sed 's/^/    /'
  echo "\$ grain access list"
  "$G" access list 2>&1 | sed 's/^/    /'
  echo "\$ grain -json access list"
  "$G" -json access list 2>&1 | jq -c '.entries[] | select(.entry | startswith("delegate:"))' | sed 's/^/    /'
)

# Cleanup processes and cluster.
pkill -f "$WALK/" 2>/dev/null || true
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
"$PGBIN/pg_ctl" -D "$WALK/pg" -m immediate stop >/dev/null 2>&1 || true
