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

# Walkthrough 2026-11-13 §9 item 1 on the daemon start-daemon.sh started:
# rc tags' release-train-assets.yml runs fire the release manager schedule
# once per open manager -- while the first is queued, while it is deferred
# and after a failed run -- and the manager holds no dispatch scope.
#
#   bash item1-release-manager.sh <root> [daemon-port]
set -euo pipefail
root="${1:?root}"
port="${2:-4450}"
sim="http://127.0.0.1:$((port + 10))"
api="http://127.0.0.1:$port"
auth="Authorization: Bearer $(cat "$root/code")"
managers() {
  curl -fsS -H "$auth" "$api/api/tasks?limit=200" |
    jq -r '[.[] | select(.title | startswith("Release manager"))] | map("#\(.id) \(.state)") | join(", ")'
}
fire() { # rc conclusion
  curl -fsS -XPOST "$sim/_tag?name=$1" >/dev/null
  echo "fired run $(curl -fsS -XPOST "$sim/_run?workflow=release-train-assets.yml&ref=$1&conclusion=$2&jobs=assets:$2") for $1 ($2)"
  sleep "${SETTLE:-15}"
}
echo "managers before: $(managers)"
first=$(curl -fsS -H "$auth" "$api/api/tasks?limit=200" | jq -r '[.[] | select(.title | startswith("Release manager"))][0].id // empty')
if [ -z "$first" ]; then
  fire v1.1.0-rc.1 success
  first=$(curl -fsS -H "$auth" "$api/api/tasks?limit=200" | jq -r '[.[] | select(.title | startswith("Release manager"))][0].id')
fi
echo "after rc.1: $(managers)"
echo "grants of #$first:"
curl -fsS -H "$auth" "$api/api/tasks/$first" | jq -r '.capabilities[]' | sed 's/^/  /'
echo "dispatch scopes among them: $(curl -fsS -H "$auth" "$api/api/tasks/$first" | jq -r '[.capabilities[] | select(test("dispatch"))] | length')"

fire v1.1.0-rc.2 failure
echo "after rc.2 with #$first queued: $(managers)"

curl -fsS -XPOST -H "$auth" -H 'Content-Type: application/json' -d '{"until":"2026-11-14T00:00:00Z","reason":"walkthrough: deferred"}' \
  "$api/api/tasks/$first/defer" >/dev/null
echo "deferred #$first: $(curl -fsS -H "$auth" "$api/api/tasks/$first" | jq -r '.state')"
fire v1.1.0-rc.3 success
echo "after rc.3 with #$first deferred: $(managers)"
echo "schedule record:"
curl -fsS -H "$auth" "$api/api/schedules" |
  jq -c '.[] | select(.id == "repo:schedule:acme:widgets:release-manager") | with_entries(select(.key | test("last|skip|Run"; "i")))'
grep -E 'release-manager|Release manager' "$root/daemon.log" | grep -viE 'seeded|declares' | tail -8 | cut -c1-400 || true
echo "every task the firings left (F1: a Stage 1 repair for release-train-assets):"
curl -fsS -H "$auth" "$api/api/tasks?limit=200" | jq -r '.[] | "  #\(.id) \(.state) p\(.priority) foundIn=\(.foundIn // "-") \(.title)"'
grep -E 'Stage 1 run' "$root/daemon.log" | sed 's/^/  /' || true
