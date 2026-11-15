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

# items_1_2.sh: §3 items 1 (upgrade with real files) and 2 (downgrade via
# GRAIN_GITHUB_CREDENTIALS=files) on the data dir seed_old.sh made.
set -uo pipefail
source "$(dirname "$0")/env.sh"
[[ -f "$WORK/ghsim.pid" ]] || start_sims
step() { printf '\n== %s\n' "$*"; }
branches() { git -C "$WORK/git/acme/widgets.git" branch --format='%(refname:short)' | grep -v '^main$' | sort; }
# write_plan F: commit F and push it through the git proxy from the run's own
# run_command, as an agent does.
write_plan() {
	jq -nc --arg f "$1" '{tools:[["run_command",{description:"push",command:("cd work && date +%s%N > "+$f+" && git add "+$f+" && git -c user.name=walk -c user.email=walk@example.com commit -qm walk && git push -q origin HEAD 2>&1; echo push-rc=$?")}]]}'
}
# wait_branch ID [SLUG]: wait for the run's branch (task ids restart on a fresh store, hence SLUG).
wait_branch() { for _ in $(seq 90); do branches | grep -q "task-$1-${2:-}" && return 0; sleep 1; done; return 1; }

step "1a. start HEAD on the schema-131 dir"
: >"$WORK/daemon.log"
start_daemon "$HEAD_BIN" && sleep 3
tree
grep -E 'moved|migrated the store' "$WORK/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200
"$HEAD_BIN" secrets -data-dir "$WORK/data" list | grep -v capability-managed
"$WORK/bin/storedigest" "$WORK/data" github-credential-bot/token secrets-repo-token/token state-repo-token/token | cut -c1-45
echo "pat file digest: $(tr -d '[:space:]' <"$WORK/values/pat" | sha256sum | cut -c1-12)"

step "1b. git proxy clones and pushes with the migrated PAT"
setup_agent
T=$(run_task "push after upgrade (PAT)" "$(write_plan pat.txt)")
wait_branch "$T" && echo "task $T branch pushed: $(branches | grep "task-$T-")" || echo "task $T: NO BRANCH"
echo "git auth schemes seen by ghsim: $(curl -s "$GHSIM/_sim/git-auth")"

step "1c. restart: nothing changes"
tree >"$WORK/tree-a"; stop_daemon; start_daemon "$HEAD_BIN" && sleep 3; tree >"$WORK/tree-b"
diff "$WORK/tree-a" "$WORK/tree-b" && echo "file tree unchanged across restart"

step "1d. recreate bot.token with a new value: taken as the newer word"
head -c 24 /dev/urandom | base64 | tr -dc A-Za-z0-9 | sed 's/^/ghp_/' >"$WORK/values/pat2"
cp "$WORK/values/pat2" "$WORK/data/secrets/github/bot.token"
stop_daemon; start_daemon "$HEAD_BIN" && sleep 3
tree | grep bot
echo "store: $("$WORK/bin/storedigest" "$WORK/data" github-credential-bot/token | cut -c1-41)  new file: $(tr -d '[:space:]' <"$WORK/values/pat2" | sha256sum | cut -c1-12)"

step "1e. App credential: tokens minted from the stored key"
stop_daemon
printf 'git:\n  - credential: ci-app\n' >"$WORK/seed-app.yaml"
"$HEAD_BIN" connections -data-dir "$WORK/data" import "$WORK/seed-app.yaml"
start_daemon "$HEAD_BIN"
m0=$(curl -s "$GHSIM/_sim/mints")
T=$(run_task "push after upgrade (App)" "$(write_plan app.txt)")
wait_branch "$T" && echo "task $T branch pushed: $(branches | grep "task-$T-")" || echo "task $T: NO BRANCH"
echo "App token mints: $m0 -> $(curl -s "$GHSIM/_sim/mints")"

step "1f. release-approval key moves on first use"
GRAIN_SERVER=$API GRAIN_TOKEN=$(cat "$WORK/data/local-api-token") "$HEAD_BIN" release approval-key 2>/dev/null | sha256sum | cut -c1-12 >"$WORK/approval-pub"
echo "public key digest $(cat "$WORK/approval-pub")"
tree | grep release
grep 'release approval: moved' "$WORK/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-160

step "2a. GRAIN_GITHUB_CREDENTIALS=files on HEAD writes everything back"
stop_daemon; start_daemon "$HEAD_BIN" GRAIN_GITHUB_CREDENTIALS=files && sleep 3
tree
grep '=files' "$WORK/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-160
"$HEAD_BIN" secrets -data-dir "$WORK/data" list | grep -v 'capability-managed\|agent-backend'
for f in sandbox-access-ca.pem package-cache-ca.pem release-approval-key.pem; do echo "$f $(digest "$WORK/data/$f")"; done | diff - "$WORK/pre-digests.txt" && echo "CA bundles and approval key byte-identical to the 61668e58 originals"
stop_daemon

step "2b. 61668e58 on the same dir"
start_daemon "$OLD_BIN" || tail -1 "$WORK/daemon.log" | sed 's/^[0-9/]* [0-9:]* //'
echo "(expected: no schema downgrade. Credentials are checked on a fresh 131 store beside the restored files.)"
mkdir -p "$WORK/head-store"
mv "$WORK/data/store" "$WORK/data/state-repo" "$WORK/data/state" "$WORK/data/store-highwater.json" "$WORK/head-store/"
"$OLD_BIN" connections -data-dir "$WORK/data" import "$WORK/seed-app.yaml"
start_daemon "$OLD_BIN" && setup_agent
m0=$(curl -s "$GHSIM/_sim/mints")
T=$(run_task "push on 61668e58 after files rollback" "$(write_plan old.txt)")
wait_branch "$T" push-on && echo "task $T branch pushed by 61668e58: $(branches | grep "task-$T-push-on")" || echo "task $T: NO BRANCH"
echo "App token mints: $m0 -> $(curl -s "$GHSIM/_sim/mints")"
GRAIN_SERVER=$API GRAIN_TOKEN=$(cat "$WORK/data/local-api-token") "$OLD_BIN" release approval-key 2>/dev/null | sha256sum | cut -c1-12 | diff - "$WORK/approval-pub" && echo "61668e58 signs with the same approval key"
for f in sandbox-access-ca.pem package-cache-ca.pem release-approval-key.pem; do echo "$f $(digest "$WORK/data/$f")"; done | diff - "$WORK/pre-digests.txt" && echo "CA bundles unchanged after 61668e58 start"
stop_daemon
rm -rf "$WORK/data/store" "$WORK/data/state-repo" "$WORK/data/state" "$WORK/data/store-highwater.json"
mv "$WORK/head-store/"* "$WORK/data/"
