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

# G=<grain binary> live_newer_schema.sh: a running daemon in read-only IaC
# mode on the HEAD export run_versions.sh left in /tmp/walk-cfg/config.git
# takes a push stamped schema 147 (a newer build's export, with template-1's
# acme/docs read gone) and is asked to sync. Before the fix it imports it
# (error null, the read gone); with it, the sync is refused and the rows kept.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
D=/tmp/walk-live; G=${G:-/home/debian/bin/grain-head}
rm -rf /tmp/walk-cfg/live.git && git clone -q --bare /tmp/walk-cfg/config.git /tmp/walk-cfg/live.git
rm -rf "$D"; mkdir -p "$D/data" "$D/sandboxes" "$D/home"; cp /tmp/walk-cfg/gemini.key "$D/"
"$G" state -data-dir "$D/data" repo -branch main file:///tmp/walk-cfg/live.git >/dev/null
trap 'stop_daemon "$D"' EXIT
GRAIN=$G start_daemon "$D"
sleep 3
dump_access "$D/data/store/grain.db" | sed -n 1,5p
c=$(mktemp -d)
git clone -q /tmp/walk-cfg/live.git "$c/live"
echo 147 >"$c/live/schema-version"
sed -i '0,/repo#read={"repo":"acme\/docs"}/{/repo#read={"repo":"acme\/docs"}/d}' "$c/live/settings/templates.yml"
git -C "$c/live" -c user.name=w -c user.email=w@example.com commit -qam 'newer build export (schema 147)'
git -C "$c/live" push -q origin main
rm -rf "$c"
curl -sS -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "{}" "$DAEMON/api/state-repo/sync" | jq -c '{error}'
sleep 2
dump_access "$D/data/store/grain.db" | sed -n 1,5p
api GET /api/state-repo | jq -c '{error, mode, head}'
