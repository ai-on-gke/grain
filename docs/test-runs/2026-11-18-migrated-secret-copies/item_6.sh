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

# item_6.sh: docs/test-plans/2026-11-18.md §5 item 6, the CA bundles'
# copies checked against Secret Manager (-secrets-backend=secretmanager),
# on SQLite. Secret Manager is STUBBED by smfakesrv.go (no emulator
# exists): pkg/secrets' own test fake run as a server.
#   a) v3.4.0 seed -> v3.13.0 (moves package-cache-ca.pem into Secret
#      Manager) -> HEAD (deletes the copy) -> HEAD (silent)
#   b) the same, with the Secret Manager secret deleted before HEAD:
#      the copy is kept and every start warns.
set -uo pipefail
ME=$(cd "$(dirname "$0")" && pwd)
export WORK=${WORK:-/var/tmp/mc/walk-sm}
V34_BIN=${V34_BIN:-/home/debian/bin/grain-v34}
V313_BIN=${V313_BIN:-/home/debian/bin/grain-v313}
source "$ME/../2026-11-11-secrets-store/env.sh"
D=$WORK/data
export GRAIN_SECRET_MANAGER_ENDPOINT=http://127.0.0.1:4277 GRAIN_SECRET_MANAGER_TOKEN=fake-sm-oauth-token
SM=(-secrets-backend secretmanager -secrets-sm-prefix projects/demo-project/secrets/grain-walk-)
step() { printf '\n== %s\n' "$*"; }
migrated() {
	find "$D" -name '*.migrated' -printf '%M %u %6s %p\n' | sed "s#$D/##" | sort -k4
	[[ -n $(find "$D" -name '*.migrated' -print -quit) ]] || echo "(no .migrated files)"
}
start() {
	local from
	from=$(($(wc -l <"$WORK/daemon.log" 2>/dev/null || echo 0) + 1))
	DAEMON_EXTRA_FLAGS="${SM[*]}" start_daemon "$@" || { echo "daemon did not come up"; sed -n "$from,\$p" "$WORK/daemon.log" | tail -3; return 1; }
	sleep 3
	stop_daemon
	echo "-- find after start:"; migrated
	echo "-- log:"
	sed -n "$from,\$p" "$WORK/daemon.log" | grep -E 'package-cache-ca|sandbox-access-ca' | sed -E 's/^[0-9\/]+ [0-9:.]+ //' | sed "s#$D/##g" | cut -c1-260
}

stop_all
rm -rf "$WORK"
start_sims
(cd "$ROOT" && go build -o "$WORK/bin/smfakesrv" "$ME/smfakesrv.go") || exit 2
"$WORK/bin/smfakesrv" >"$WORK/sm.log" 2>&1 &
echo $! >"$WORK/sm.pid"
step "seed with v3.4.0 (file store; its first start writes package-cache-ca.pem)"
start_daemon "$V34_BIN" >/dev/null && stop_daemon
migrated; ls -l "$D"/*.pem | awk '{print $1, $5, $NF}' | sed "s#$D/##"
step "a1. v3.13.0 on Secret Manager"
start "$V313_BIN"
cp -a "$D" "$WORK/data-after-v313"
step "a2. HEAD on Secret Manager"
start "$HEAD_BIN"
step "a3. HEAD again"
start "$HEAD_BIN"

step "b. the Secret Manager secret deleted before HEAD"
rm -rf "$D" && cp -a "$WORK/data-after-v313" "$D"
# With v3.13.0's CLI: HEAD's `grain secrets` on Secret Manager opens the
# database for its catalogue and migrates the store to 152 without the
# state repository, after which HEAD's daemon refuses to start (finding F3).
"$V313_BIN" secrets -data-dir "$D" "${SM[@]}" delete package-cache-ca
"$V313_BIN" secrets -data-dir "$D" "${SM[@]}" list | grep -i 'cache' || echo "(store holds no package-cache-ca)"
step "b1. HEAD"
start "$HEAD_BIN"
step "b2. HEAD again: the warning repeats"
start "$HEAD_BIN"
kill "$(cat "$WORK/sm.pid")" 2>/dev/null
stop_all
