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

# §1 item 2: catalog.scope_owners. Seeds primary claims by bob (not an acme
# owner) with no policy, restarts both replicas with the policy, then checks
# new claims, demotion on directory/route/heartbeat and the host override.
#   phase=seed ./scope-owners-walk.sh ; phase=enforce ./scope-owners-walk.sh
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/lib.sh"
reg() { # reg <who> <entry> <scope args...>: register a connected entry via replica A
  local who=$1 entry=$2
  shift 2
  echo "\$ [$who @ 4251] grain registry register -standalone -mint-token -listed $* $entry"
  "$G" registry register -standalone -registry-url "$RA" -registry-token "$(fakejwt "$who")" \
    -mint-token -token-file "$WALK/$entry.token" -listed "$@" "$entry" 2>&1 | grep -v '^ \|snippet' | sed 's/^/    /'
  echo "    (exit ${PIPESTATUS[0]})"
}
heartbeat() { # heartbeat <entry> <repo:role>...: a daemon heartbeat declaring scopes, on the public listener
  local entry=$1 scopes="" s
  shift
  for s in "$@"; do scopes="$scopes{\"repo\":\"${s%%:*}\",\"role\":\"${s##*:}\"},"; done
  echo "\$ PUT :4253/api/entries/$entry/heartbeat scopes=[$*]"
  curl -sS -w '\n    HTTP %{http_code}\n' -X PUT -H "Authorization: Bearer $(cat "$WALK/$entry.token")" \
    -H 'Content-Type: application/json' -d "{\"health\":\"ok\",\"scopes\":[${scopes%,}]}" \
    "http://127.0.0.1:4253/api/entries/$entry/heartbeat" | sed 's/^/    /'
}
routes() {
  for q in acme/billing github.com/acme/billing acme/payments github.com/acme/payments; do
    as alice@example.com $RB route "$q"
  done
  HEADC=1500 api alice@example.com $RB GET '/api/directory?repo=acme/billing'
}

case "${phase:-seed}" in
seed)
  echo "=== seed (no catalog.scope_owners): bob claims acme primaries"
  reg bob@example.com bob-ws -scope acme/billing:primary -scope github.com/acme/payments:primary
  routes
  ;;
enforce)
  echo "=== startup sweep (both replicas restarted with catalog.scope_owners)"
  grep -h "scope_owners" "$WALK"/logs/registry-a.log "$WALK"/logs/registry-b.log | sed 's/^/    /'
  echo "=== (b) pre-existing unauthorized primaries on directory and route"
  as bob@example.com $RB show bob-ws
  routes
  echo "=== (b) connected-entry heartbeat re-declaring the primaries"
  heartbeat bob-ws acme/billing:primary github.com/acme/payments:primary
  as alice@example.com $RB route acme/billing
  as alice@example.com $RB route github.com/acme/payments
  echo "=== (a) new primary claims by non-owners"
  as bob@example.com $RA scopes -set acme/repo:primary bob-ws
  as bob@example.com $RA scopes -set github.com/acme/repo:primary bob-ws
  as bob@example.com $RA scopes -set GitHub.com/ACME/repo:primary bob-ws
  as bob@example.com $RA scopes -set acme/repo:secondary bob-ws
  as bob@example.com $RA create -name bob-vm -shape standard -scope acme/repo:primary
  as bob@example.com $RA create -name bob-vm -shape standard -scope github.com/acme/repo:primary
  reg bob@example.com bob-ws2 -scope github.com/acme/repo:primary
  echo "=== owners may claim"
  reg carol@example.com carol-ws -scope acme/repo:primary
  as carol@example.com $RA scopes -set acme/repo:primary -set github.com/acme/other:primary carol-ws
  as alice@example.com $RB route github.com/acme/repo
  echo "=== (c) gitlab.example.com/acme/* host override"
  reg alt@example.com alt-ws -scope gitlab.example.com/acme/repo:primary
  as carol@example.com $RA scopes -set gitlab.example.com/acme/repo2:primary carol-ws
  as alt@example.com $RA scopes -set acme/repo3:primary alt-ws
  as alice@example.com $RB route gitlab.example.com/acme/repo
  ;;
esac
