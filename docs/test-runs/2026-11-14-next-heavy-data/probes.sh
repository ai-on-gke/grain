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

# docs/test-plans/2026-11-14.md §3 item 4: a real daemon whose git host is
# the 2026-11-13 GitHub simulator behind slowproxy.py (listings and branch
# lists held 3 s), so concurrent Settings tabs can be counted against the
# upstream calls they cause. Leaves everything running; `probes.sh down`
# stops it.
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
WORK=${WORK:-/tmp/heavy-probes}
PORT=${PORT:-4288}

down() {
	for p in daemon proxy ghsim; do
		[[ -f $WORK/$p.pid ]] && kill "$(cat "$WORK/$p.pid")" 2>/dev/null || true
	done
	sleep 1
}
if [[ ${1:-} == down ]]; then down; exit 0; fi
down
rm -rf "$WORK"; mkdir -p "$WORK/data" "$WORK/sb" "$WORK/bares"
(cd "$ROOT" && go build -o "$WORK/ghsim" ./docs/test-runs/2026-11-13-next-ui-complete/ghsim.go)
for r in widgets api; do
	git init -q --bare -b main "$WORK/bares/$r.git"
	git -C "$WORK/bares/$r.git" config http.receivepack true
	tmp=$(mktemp -d); git -C "$tmp" init -q -b main
	echo "# acme/$r" >"$tmp/README.md"; git -C "$tmp" add . ; git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm seed
	git -C "$tmp" push -q "$WORK/bares/$r.git" main; rm -rf "$tmp"
done
"$WORK/ghsim" -addr 127.0.0.1:4294 -bares-dir "$WORK/bares" -repos widgets,api -token ghp_walkthrough >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"
python3 "$HERE/slowproxy.py" 4297 4294 "$WORK/upstream.log" 3 >"$WORK/proxy.log" 2>&1 &
echo $! >"$WORK/proxy.pid"
echo ghp_walkthrough >"$WORK/bot.token"
printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK/bot.token" >"$WORK/git.yaml"
"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/git.yaml" >"$WORK/connections.log" 2>&1
GCE_METADATA_HOST=127.0.0.1:9 "$GRAIN" daemon -sandbox-port-base=0 -host-sandboxes -data-dir "$WORK/data" -sandbox-dir "$WORK/sb" \
	-ui-addr "127.0.0.1:$PORT" -as alice@example.com -deployment-owner alice@example.com -repo acme/widgets -repo acme/api \
	-github-host 127.0.0.1:4297 -github-insecure-http -poll-interval 1h >"$WORK/daemon.log" 2>&1 &
echo $! >"$WORK/daemon.pid"
for _ in $(seq 100); do
	[[ -s $WORK/data/local-api-token ]] && curl -fsS -o /dev/null -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" "127.0.0.1:$PORT/api/config" 2>/dev/null && { echo up; exit 0; }
	sleep 0.2
done
tail -20 "$WORK/daemon.log"; exit 1
