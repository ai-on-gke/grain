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

# setup.sh: a fresh §11 environment. Kills any previous forge/daemon,
# makes an empty bare acme/grain-config.git under $W/forge, serves it
# with forge.go on 127.0.0.1:4291, starts `grain daemon` (GRAIN, default
# /home/debian/bin/grain) on 127.0.0.1:4292 with -github-host pointed at
# the forge (and the API socket mapped to carol@example.com), names the bare repository as the external config repository
# (an explicit token: the forge is not github.com), and seeds it with the
# deployment's own export so the first sync applies it.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
W=${W:-/home/debian/w11}
GRAIN=${GRAIN:-/home/debian/bin/grain}
pkill -f "$W/forge-bin" || true
pkill -f "grain daemon -data-dir $W/data" || true
sleep 2
rm -rf "$W/forge" "$W/data" "$W/clone" "$W"/*.log
mkdir -p "$W/forge/acme" "$W/data"
git init -q --bare -b main "$W/forge/acme/grain-config.git"
git -C "$W/forge/acme/grain-config.git" config http.receivepack true
echo '[]' >"$W/forge/pulls.json"
(cd "$here" && go build -o "$W/forge-bin" forge.go)
nohup "$W/forge-bin" -addr 127.0.0.1:4291 -root "$W/forge" -pulls "$W/forge/pulls.json" >"$W/forge.log" 2>&1 &
nohup "$GRAIN" daemon -data-dir "$W/data" -ui-addr 127.0.0.1:4292 -sandbox-port-base=0 \
	-github-host 127.0.0.1:4291 -github-insecure-http -disable-native-sandboxes \
	-host-accounts "$(id -u)=carol@example.com" >"$W/daemon.log" 2>&1 &
for _ in $(seq 60); do
	curl -s -o /dev/null http://127.0.0.1:4292/api/state-repo && [ -s "$W/data/local-api-token" ] && break
	sleep 1
done
# The external agent API on (item 1's agent caller), and the seed taken
# only once the deployment's config row is in its export -- an export
# taken earlier has an empty tables/grain_config.json, and importing that
# leaves the deployment with no stored configuration at all.
curl -s -o /dev/null -H "Authorization: Bearer $(cat "$W/data/local-api-token")" -X PUT \
	http://127.0.0.1:4292/api/settings -d '{"externalAgentApi":true}'
for _ in $(seq 60); do
	grep -q "\"external_agent_api\": *true" "$W/data/state-repo/tables/grain_config.json" 2>/dev/null && break
	sleep 1
done
git clone -q "$W/forge/acme/grain-config.git" "$W/clone" 2>/dev/null
git -C "$W/clone" config user.name "Walk Through"
git -C "$W/clone" config user.email walk@example.com
git -C "$W/data/state-repo" archive HEAD | tar x -C "$W/clone"
git -C "$W/clone" add -A
git -C "$W/clone" commit -qm "Seed grain-config from the deployment"
git -C "$W/clone" push -q origin main
. "$here/lib.sh"
# carol, whom the API socket names, holds a limited role.
api POST /api/access/principals '{"entry":"user:carol@example.com","role":"grain/viewer","reason":"walkthrough limited role"}' >/dev/null
set_remote '{"token":"forge-token"}' | jq -c '{mode, head}'
sleep 13
echo "seeded: $(state)"
