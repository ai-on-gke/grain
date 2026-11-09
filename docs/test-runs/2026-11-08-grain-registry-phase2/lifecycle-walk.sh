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

# §1 item 3: -disk-gb bounds, stop/start, two-step transfer expiry and
# revocation, delete/restore/purge. compute.provider is none, so the walk
# seeds one shape and plays the reconciler's part in PostgreSQL (moving
# lifecycle_state) where a real one would.
#   part=disk|transfer|purge ./lifecycle-walk.sh   (SFX=<x> renames the VMs for a re-run)
# Each part restarts both replicas first: a replica allows a person ten
# control-plane mutations a minute, and the walk makes more than that.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/lib.sh"
sql() { echo "  sql> $1"; "${PSQL[@]}" -c "$1" | sed 's/^/    /'; }
"${PSQL[@]}" -c "insert into registry.shapes(name,machine_type,boot_disk_gb,description) values ('standard','e2-standard-4',50,'walk shape') on conflict do nothing" >/dev/null

SCOPE_OWNERS=1 RESTART=1 "$HERE/env-up.sh" >/dev/null
sleep 3
V=${SFX:-}
case "${part:-disk}" in
disk)
echo "=== -disk-gb bounds (CLI and server)"
as alice@example.com $RB create -name alice-vm$V -shape standard -disk-gb 20
as carol@example.com $RA create -name carol-vm$V -shape standard -disk-gb 19
as carol@example.com $RA create -name carol-vm$V -shape standard -disk-gb 4001
api carol@example.com $RA POST /api/entries "{\"name\":\"carol-vm$V\",\"kind\":\"instance\",\"shape\":\"standard\",\"boot_disk_gb\":19}"
api carol@example.com $RA POST /api/entries "{\"name\":\"carol-vm$V\",\"kind\":\"instance\",\"shape\":\"standard\",\"boot_disk_gb\":4001}"
as carol@example.com $RA create -name carol-vm$V -shape standard -disk-gb 4000
as carol@example.com $RB create -name carolvm2$V -shape standard
sql "select name, lifecycle_state, boot_disk_gb from registry.entries where name in ('alice-vm$V','carol-vm$V','carolvm2$V') order by name"
sql "update registry.entries set lifecycle_state='running' where name in ('alice-vm$V','carol-vm$V')"
as alice@example.com $RA resize -disk-gb 19 alice-vm$V
as alice@example.com $RA resize -disk-gb 4001 alice-vm$V
api alice@example.com $RA POST /api/entries/alice-vm$V/replace '{"boot_disk_gb":4001}'
as alice@example.com $RA resize -disk-gb 200 alice-vm$V
as alice@example.com $RA resize -disk-gb 200 conn-alice
sql "select name, lifecycle_state, boot_disk_gb from registry.entries where name='alice-vm$V'"

echo "=== stop / start"
sql "update registry.entries set lifecycle_state='running', replace_step='' where name='alice-vm$V'"
as bob@example.com $RA stop alice-vm$V
as alice@example.com $RA stop conn-alice
as alice@example.com $RA start conn-alice
as alice@example.com $RA stop alice-vm$V
as alice@example.com $RB show alice-vm$V
as alice@example.com $RA stop alice-vm$V
sql "update registry.entries set lifecycle_state='stopped', stopped_at=now() where name='alice-vm$V'"
as alice@example.com $RB list
as alice@example.com $RA start alice-vm$V
as alice@example.com $RB start alice-vm$V

;;
transfer)
echo "=== two-step transfer of conn-alice (live tunnel) to carol"
OLD_TOKEN=$(cat "$WALK/conn-alice.token")
as alice@example.com $RA transfer -to carol@example.com conn-alice
as alice@example.com $RA transfer -cancel conn-alice
as carol@example.com $RA accept-transfer conn-alice
as alice@example.com $RA transfer -to carol@example.com conn-alice
sql "select pending_owner, transfer_expires_at > now() + interval '6 days' as seven_day_expiry from registry.entries where name='conn-alice'"
sql "update registry.entries set transfer_expires_at = now() - interval '1 minute' where name='conn-alice'"
as carol@example.com $RB accept-transfer conn-alice
as carol@example.com $RB show conn-alice
echo "--- fresh offer + accept (tunnel and grc_ credential must go)"
as alice@example.com $RA transfer -to carol@example.com conn-alice
echo "  before accept: $(curl -sS -o /dev/null -w '%{http_code}' -H 'Host: conn-alice.grain-user.test' -H 'X-Test-Email: alice@example.com' $RB/api/whoami)"
as carol@example.com $RB accept-transfer conn-alice
sleep 1
echo "  old grc_ heartbeat after accept: $(curl -sS -o /tmp/gr2-hb.out -w '%{http_code}' -X PUT -H "Authorization: Bearer $OLD_TOKEN" -H 'Content-Type: application/json' -d '{"health":"ok"}' http://127.0.0.1:4253/api/entries/conn-alice/heartbeat) $(head -c 160 /tmp/gr2-hb.out)"
echo "  carol (new owner) via tunnel after accept: $(curl -sS -o /tmp/gr2-t.out -w '%{http_code}' -H 'Host: conn-alice.grain-user.test' -H 'X-Test-Email: carol@example.com' $RB/api/whoami) $(head -c 200 /tmp/gr2-t.out)"
echo "  registry tunnel state: $("${PSQL[@]}" -c "select coalesce(last_disconnect_reason,'') || ' at ' || coalesce(last_disconnect_at::text,'') from registry.entries where name='conn-alice' and deleted_at is null")"
echo "  alice via tunnel after accept: $(curl -sS -o /tmp/gr2-t.out -w '%{http_code}' -H 'Host: conn-alice.grain-user.test' -H 'X-Test-Email: alice@example.com' $RB/api/whoami) $(head -c 160 /tmp/gr2-t.out)"
echo "  daemon log:"
grep "registry tunnel\|registry heartbeat" "$WALK/logs/d1.log" | tail -n 3 | sed 's/^/    /'
echo "--- admin force-transfer"
api bob@example.com $RA POST /api/admin/entries/conn-alice/transfer '{"to":"alice@example.com"}'
api root@example.com $RA POST /api/admin/entries/conn-alice/transfer '{"to":"alice@example.com"}'
sql "select owner_email, pending_owner, transfer_expires_at from registry.entries where name='conn-alice'"
sql "select actor_email, action, entry_name from registry.audit_log where action like '%transfer%' order by at desc limit 4"

;;
purge)
echo "=== delete / restore / purge"
as carol@example.com $RA delete -yes carol-ws
as carol@example.com $RA restore carol-ws
as carol@example.com $RA delete -yes carol-ws
api carol@example.com $RA POST /api/admin/entries/carol-ws/purge '{}'
api root@example.com $RA POST /api/admin/entries/carol-ws/purge '{}'
as carol@example.com $RA restore carol-ws
api bob@example.com $RA GET /api/names/carol-ws
reg_try() { "$G" registry register -standalone -registry-url "$RA" -registry-token "$(fakejwt "$1")" -mint-token -token-file "$WALK/$2.token2" "$2" 2>&1 | head -n 2 | sed 's/^/    /'; }
echo "  bob registers carol-ws (reservation kept):"
reg_try bob@example.com carol-ws
as alt@example.com $RA delete -yes alt-ws
api root@example.com $RA POST /api/admin/entries/alt-ws/purge '{"release_reservation":true}'
as alt@example.com $RA restore alt-ws
api bob@example.com $RA GET /api/names/alt-ws
echo "  bob registers alt-ws (reservation released):"
reg_try bob@example.com alt-ws
api root@example.com $RA POST /api/admin/entries/alice-vm$V/purge '{}'
echo "--- db_purge_after expiry"
as alice@example.com $RA delete -yes carolvm2$V 2>/dev/null
as carol@example.com $RA delete -yes carolvm2$V
sql "update registry.entries set db_purge_after = now() - interval '1 hour' where name='carolvm2$V'"
as carol@example.com $RA restore carolvm2$V
sql "select name, deleted_at is not null as deleted, purged_at is not null as purged, reserved_until is not null as reserved from registry.entries where name in ('carol-ws','alt-ws','carolvm2$V') order by name, deleted_at"
;;
esac
