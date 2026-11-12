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

# item1-install.sh: §9 item 1. As the second Linux user, run the
# controller-served installer (`/hostd/install --agentd`) against the
# local daemon, accept the join as the owner, and start grain-agentd by
# hand (the sandbox has no systemd --user for walkhost, so --no-start).
set -euo pipefail

ROOT=${ROOT:-/var/tmp/hac}
ADDR=${ADDR:-127.0.0.1:4291}
HOSTUSER=${HOSTUSER:-walkhost}
NAME=${NAME:-walk}
T=$(cat "$ROOT/data/local-api-token")
H=$(getent passwd "$HOSTUSER" | cut -d: -f6)
api() { curl -fsS -H "Authorization: Bearer $T" "$@"; }

code=$(GRAIN_SERVER="http://$ADDR" GRAIN_TOKEN=$T /home/debian/bin/grain host invite -name "$NAME-$(date +%s)" |
	grep -oE 'ghe_[A-Za-z0-9_-]+' | head -1)

curl -fsSL "http://$ADDR/hostd/install" >"$ROOT/hostd_install.sh"
chmod 644 "$ROOT/hostd_install.sh"
sudo -u "$HOSTUSER" -H env PATH="$H/.local/bin:/usr/bin:/bin" \
	sh "$ROOT/hostd_install.sh" --agentd --join "$code" --name "$NAME" --no-start \
	>"$ROOT/item1-install.out" 2>&1 &
inst=$!

id=""
for _ in $(seq 1 100); do
	id=$(api "http://$ADDR/api/hostds/pending" | jq -r '.[0].id // empty')
	[ -n "$id" ] && break
	sleep 0.2
done
[ -n "$id" ] || { echo "no pending join"; cat "$ROOT/item1-install.out"; exit 1; }
api "http://$ADDR/api/hostds/pending" >"$ROOT/item1-pending.json"
api -X POST -H 'Content-Type: application/json' -d "{\"name\":\"$NAME\"}" \
	"http://$ADDR/api/hostds/pending/$id/accept" >/dev/null
wait "$inst"
cat "$ROOT/item1-install.out"
echo "--- allowlist as written"
sudo cat "$H/.config/grain/agentd.yaml"
