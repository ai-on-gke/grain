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

# §1 item 1: delegating apps + consent grants through the compiled `grain`
# CLI, delegated on_behalf_of traffic through both replicas into the
# connected daemon, and cross-replica eviction timing.
# Expects env-up.sh and start-daemon.sh to have run.
set -uo pipefail
. "$(dirname "$0")/lib.sh"

# until_refused <registry> <entry> <who> [path] [method]: poll a delegated
# request every 25ms until the registry refuses it (401/403); print the
# elapsed time. A 404 from the daemon still means the request got through.
until_refused() {
  local t0 t1 code
  t0=$(date +%s%N)
  for _ in $(seq 1 400); do
    code=$(curl -sS -o /tmp/gr2-obo.out -w '%{http_code}' -X "${5:-GET}" -H "Host: $2.grain-user.test" \
      -H "X-Test-Email: $SA" -H "X-Grain-On-Behalf-Of: $3" "$1${4:-/api/whoami}")
    case $code in 401 | 403) break ;; esac
    sleep 0.025
  done
  t1=$(date +%s%N)
  echo "    replica ${1##*:} refused after $(((t1 - t0) / 1000000))ms: $code $(head -c 160 /tmp/gr2-obo.out)"
}
warm() { # warm both replicas' assertion caches
  echo "  warm A: $(obo $RA conn-alice alice@example.com)"
  echo "  warm B: $(obo $RB conn-alice alice@example.com)"
  echo "  warm B: $(obo $RB conn-alice alice@example.com)"
}

echo "=== delegating apps (registry admins only)"
as root@example.com $RA delegating-apps list
as alice@example.com $RA delegating-apps create -principal "$SA" ci-bot
as root@example.com $RA delegating-apps create -principal alice@example.com human-app
as root@example.com $RA delegating-apps create -principal "$SA" -display-name "CI bot" -max-access write -max-grant-ttl 48h ci-bot
as root@example.com $RA delegating-apps create -principal serviceAccount:other-bot@walk-proj.iam.gserviceaccount.com -max-access read -max-grant-ttl 1h reader-app
as root@example.com $RB delegating-apps list
echo "  stored principals: $("${PSQL[@]}" -c "select string_agg(name||'='||principal||' max_access='||max_access||' ttl='||max_grant_ttl_seconds, ', ' order by name) from registry.delegating_apps")"

echo "=== consent grants"
# Grant creation goes through replica B and revocation/narrowing through A:
# each replica allows a person 10 control-plane mutations a minute.
as alice@example.com $RB grants create -entry conn-alice -access write reader-app
as alice@example.com $RB grants create -entry conn-alice -ttl 10h reader-app
as alice@example.com $RB grants create -entry conn-alice -access read ci-bot
as alice@example.com $RB grants list
as bob@example.com $RB grants list

echo "=== delegated traffic through both replicas (read grant)"
echo "  A GET  whoami: $(obo $RA conn-alice alice@example.com)"
echo "  B GET  whoami: $(obo $RB conn-alice alice@example.com)"
echo "  B POST tasks:  $(obo $RB conn-alice alice@example.com /api/tasks POST)"
echo "  B GET  for bob (no grant): $(obo $RB conn-alice bob@example.com)"
echo "  reader-app (granted, but not on the daemon's delegate: list): $(OBO_SA=other-bot@walk-proj.iam.gserviceaccount.com obo $RB conn-alice alice@example.com)"

echo "=== (a) upsert to write, warm, then narrow back to read on A; B must refuse writes"
as alice@example.com $RB grants create -entry conn-alice -access write ci-bot
echo "  B POST (write grant): $(HEADC=120 obo $RB conn-alice alice@example.com /api/tasks/does-not-exist/cancel POST)"
warm
as alice@example.com $RA grants create -entry conn-alice -access read ci-bot
until_refused $RB conn-alice alice@example.com /api/tasks/does-not-exist/cancel POST
echo "=== (a') narrow entries away from conn-alice on A; B must refuse reads"
warm
as alice@example.com $RA grants create -entry some-other-entry -access read ci-bot
until_refused $RB conn-alice alice@example.com

echo "=== (b) revoke on A by dlg_ ID; B must refuse"
as alice@example.com $RB grants create -entry conn-alice -access write ci-bot
GID=$("${PSQL[@]}" -c "select id from registry.delegation_grants where app='ci-bot' and person='alice@example.com' and revoked_at is null")
echo "  live grant: $GID"
warm
as bob@example.com $RA grants revoke "$GID"
as bob@example.com $RA grants revoke ci-bot
echo "  B still serves alice after bob's refused revokes: $(obo $RB conn-alice alice@example.com)"
as alice@example.com $RA grants revoke "$GID"
until_refused $RB conn-alice alice@example.com
echo "=== (b') re-grant, warm, revoke by app name on A"
as alice@example.com $RB grants create -entry '*' -access write ci-bot
warm
as alice@example.com $RA grants revoke ci-bot
until_refused $RB conn-alice alice@example.com
as alice@example.com $RA grants list -include-revoked

echo "=== (c) re-grant, warm, disable app on A; B must refuse; enable restores"
as alice@example.com $RB grants create -entry conn-alice -access write ci-bot
warm
as root@example.com $RA delegating-apps disable ci-bot
until_refused $RB conn-alice alice@example.com
as alice@example.com $RB grants create -entry conn-alice ci-bot
as root@example.com $RA delegating-apps enable ci-bot
echo "  B after enable (grant was revoked by nothing, app re-enabled): $(obo $RB conn-alice alice@example.com)"

echo "=== delegating app vs human-only stop/start and grant APIs"
api "$SA" $RA POST /api/entries/conn-alice/stop
api "$SA" $RA POST /api/entries/conn-alice/start
api "$SA" $RA GET /api/delegation-grants
echo "  SA stop with on-behalf-of on registry host:"
curl -sS -w '    HTTP %{http_code}\n' -X POST -H "X-Test-Email: $SA" -H "X-Grain-On-Behalf-Of: alice@example.com" $RA/api/entries/conn-alice/stop | sed 's/^/    /'

echo "=== downstream delegate: allowlist on the connected daemon"
export GRAIN_SERVER=http://127.0.0.1:4270 GRAIN_DATA_DIR=$WALK/d1
"$G" access list | sed 's/^/    /'
echo "  allowed: $(obo $RB conn-alice alice@example.com)"
"$G" access deny "delegate:$SA" | sed 's/^/    /'
echo "  after deny: $(obo $RB conn-alice alice@example.com)"
"$G" access allow --label ci-bot --role grain/delegated --reason "walk delegated ci-bot" "delegate:$SA" | sed 's/^/    /'
echo "  re-allowed: $(obo $RB conn-alice alice@example.com)"
