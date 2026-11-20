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

# walk.sh DB: docs/test-plans/2026-11-18.md §5 on one database backend
# (DB=sqlite or postgres). It steps one data directory through
# v3.4.0 (pre-move seed) -> v3.13.0 (makes the .migrated copies) -> HEAD
# -> HEAD again, then walks items 2-5. It reuses the 2026-11-11
# secrets-store harness (env.sh: ghsim, fake-agy.sh, hook.py, a real
# `grain daemon` with -host-sandboxes and -sandbox-port-base=0).
#
#   V34_BIN, V313_BIN, HEAD_BIN, UNFIXED_BIN  the four builds
#   V34_SRC                                   a v3.4.0 worktree (seedca.go)
#
# Secret values are random, live in $WORK/values and are never printed:
# the store is compared by sha256 prefix (storedigest.go).
set -uo pipefail
DB=${1:?sqlite or postgres}
ME=$(cd "$(dirname "$0")" && pwd)
export WORK=${WORK:-/var/tmp/mc/walk-$DB}
V34_BIN=${V34_BIN:-/home/debian/bin/grain-v34}
V313_BIN=${V313_BIN:-/home/debian/bin/grain-v313}
UNFIXED_BIN=${UNFIXED_BIN:-/home/debian/bin/grain-unfixed}
V34_SRC=${V34_SRC:-/var/tmp/mc/v34}
source "$ME/../2026-11-11-secrets-store/env.sh"
PGURL="postgres://walk:walk@127.0.0.1:5432/mc_walk?sslmode=disable"
if [[ $DB == postgres ]]; then
	export DAEMON_EXTRA_FLAGS="-postgres-url $PGURL"
	# `grain connections` and `grain secrets` open the same database.
	export GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN=$PGURL
else
	export DAEMON_EXTRA_FLAGS=""
fi
D=$WORK/data

step() { printf '\n== %s\n' "$*"; }
# migrated lists every .migrated copy under the data dir (the plan's
# `find <data-dir> -name '*.migrated' -ls`, without inode/block columns).
migrated() {
	find "$D" -name '*.migrated' -printf '%M %u %6s %p\n' | sed "s#$D/##" | sort -k4
	[[ -n $(find "$D" -name '*.migrated' -print -quit) ]] || echo "(no .migrated files)"
}
# loglines prints this start's lines about copies and moves, with
# timestamps stripped and the data dir shortened.
loglines() {
	sed -n "$1,\$p" "$WORK/daemon.log" | grep -E 'rollback copy|relay token copy|moved .* into|into the secrets store|=files|WARNING: keeping|neither read nor deleted|schema is newer|panic' |
		sed -E 's/^[0-9\/]+ [0-9:.]+ //' | sed "s#$D/##g" | cut -c1-260
}
# start BIN [ENV...]: start a daemon, list the copies and this start's log lines.
start() {
	local from
	from=$(($(wc -l <"$WORK/daemon.log" 2>/dev/null || echo 0) + 1))
	if ! start_daemon "$@"; then
		echo "daemon did not come up:"; sed -n "$from,\$p" "$WORK/daemon.log" | tail -5 | cut -c1-260
		return 1
	fi
	sleep 3
	echo "-- find $D -name '*.migrated' after start:"; migrated
	echo "-- log:"; loglines "$from"
}
restart() { stop_daemon; start "$@"; }
digests() { "$WORK/bin/storedigest" "$D" "$@" | cut -c1-60; }
vdigest() { tr -d '[:space:]' <"$WORK/values/$1" | sha256sum | cut -c1-12; }
branches() { git -C "$WORK/git/acme/widgets.git" branch --format='%(refname:short)' | grep -v '^main$' | sort; }
write_plan() {
	jq -nc --arg f "$1" '{tools:[["run_command",{description:"push",command:("cd work && date +%s%N > "+$f+" && git add "+$f+" && git -c user.name=walk -c user.email=walk@example.com commit -qm walk && git push -q origin HEAD 2>&1; echo push-rc=$?")}]]}'
}
# push_check LABEL: a run commits and pushes through the git proxy.
push_check() {
	setup_agent
	local t
	t=$(run_task "push $1" "$(write_plan "$1.txt")")
	for _ in $(seq 90); do branches | grep -q "task-$t-" && break; sleep 1; done
	if branches | grep -q "task-$t-"; then echo "push $1: task $t pushed $(branches | grep "task-$t-")"; else echo "push $1: task $t NO BRANCH"; fi
}
approval_key() {
	GRAIN_SERVER=$API GRAIN_TOKEN=$(cat "$D/local-api-token") "$1" release approval-key 2>/dev/null | sha256sum | cut -c1-12
}
rnd() { head -c 24 /dev/urandom | base64 | tr -dc A-Za-z0-9 | sed 's/^/ghp_/'; }
db_reset() {
	[[ $DB == postgres ]] || return 0
	sudo -u postgres psql -q -c "drop database if exists mc_walk" -c "create database mc_walk owner walk" >/dev/null
}
db_backup() {
	if [[ $DB == postgres ]]; then pg_dump -Fc -f "$WORK/backup.dump" "$PGURL"; else rm -rf "$WORK/backup-store"; cp -a "$D/store" "$WORK/backup-store"; fi
}
db_restore() {
	if [[ $DB == postgres ]]; then
		echo "pg_restore --clean --if-exists --no-owner (docs/host-setup.md, as written):"
		pg_restore --clean --if-exists --no-owner -d "$PGURL" "$WORK/backup.dump" 2>&1 | grep -E '^pg_restore: (error|warning)' | cut -c1-160
		echo "into an emptied schema instead (DROP SCHEMA public CASCADE; CREATE SCHEMA public):"
		psql "$PGURL" -q -c 'DROP SCHEMA public CASCADE' -c 'CREATE SCHEMA public' 2>/dev/null
		pg_restore --no-owner -d "$PGURL" "$WORK/backup.dump" && echo "pg_restore: no errors"
	else mv "$D/store" "$WORK/store-head-$(date +%s)"; cp -a "$WORK/backup-store" "$D/store"; fi
}

step "0. seed with v3.4.0 ($DB)"
stop_all
rm -rf "$WORK"
db_reset
start_sims
rnd >"$WORK/values/pat"
printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK/values/pat" >"$WORK/seed.yaml"
"$V34_BIN" connections -data-dir "$D" import "$WORK/seed.yaml" >/dev/null
start_daemon "$V34_BIN" || { tail -5 "$WORK/daemon.log"; exit 1; }
stop_daemon
(cd "$V34_SRC" && cp "$HERE/seedca.go" seedca_tmp.go && go run seedca_tmp.go "$D"; rm -f seedca_tmp.go)
ssh-keygen -q -t ed25519 -N '' -f "$WORK/values/ssh_key" -C walk
jq -n --rawfile k "$WORK/values/ssh_key" '{host:"127.0.0.1",port:2222,user:"git",private_key:$k}' >"$D/secrets/github/deploy-ssh.ssh.json"
rnd >"$WORK/values/secrets-repo-token"
rnd >"$WORK/values/state-repo-token"
rnd >"$WORK/values/relay-token"
git init -q --bare -b main "$WORK/git/acme/grain-secrets.git"
git -C "$WORK/git/acme/grain-secrets.git" config http.receivepack true
start_daemon "$V34_BIN"
api POST /api/state-repo/secrets-repo "$(jq -n --rawfile t "$WORK/values/secrets-repo-token" --arg r "http://$GHSIM/acme/grain-secrets.git" '{remote:$r,token:($t|rtrimstr("\n"))}')" >/dev/null
stop_daemon
cp "$WORK/values/state-repo-token" "$D/secrets/state-repo-token"
mkdir -p "$D/secrets/relay" && cp "$WORK/values/relay-token" "$D/secrets/relay/token"
chmod 600 "$D/secrets/github/"* "$D/secrets/state-repo-token" "$D/secrets/relay/token" "$WORK/values/"*
for f in sandbox-access-ca.pem package-cache-ca.pem release-approval-key.pem; do echo "$f $(digest "$D/$f")"; done >"$WORK/pre-digests.txt"
mkdir -p "$WORK/pre" && cp "$D/sandbox-access-ca.pem" "$D/package-cache-ca.pem" "$D/release-approval-key.pem" "$WORK/pre/" && chmod 600 "$WORK/pre/"*
tree
cat "$WORK/pre-digests.txt"

step "1a. first start on v3.13.0 (the move: makes the .migrated copies)"
: >"$WORK/daemon.log"
start "$V313_BIN"
push_check v313
echo "approval key (v3.13.0, moves on first use): $(approval_key "$V313_BIN" | tee "$WORK/approval-pub")"
stop_daemon
echo "-- after the approval-key use:"; migrated
db_backup
cp -a "$D" "$WORK/data-after-v313"

step "1b. second v3.13.0 start (copies stay: v3.13.0 never deletes them)"
start "$V313_BIN"

step "2a. rotate bot's token through PUT /api/github-tokens/bot (the /next/settings route) on v3.13.0"
rnd >"$WORK/values/pat2"
api PUT /api/github-tokens/bot "$(jq -n --rawfile v "$WORK/values/pat2" '{value:($v|rtrimstr("\n"))}')"; echo
echo "-- after the rotation:"; migrated
stop_daemon
echo "superseded copy, the offline way: rotate state-repo-token with grain secrets set (daemon stopped)"
rnd >"$WORK/values/state-repo-token2"
"$V313_BIN" secrets -data-dir "$D" set -value-file "$WORK/values/state-repo-token2" state-repo-token token >/dev/null
echo "orphaned copy: delete github-credential-deploy-ssh from the store (daemon stopped)"
"$V313_BIN" secrets -data-dir "$D" delete github-credential-deploy-ssh >/dev/null
"$HEAD_BIN" secrets -data-dir "$D" list | grep -v 'capability-managed\|agent-backend'

step "1c. first HEAD start: copies the store holds go, logged once"
start "$HEAD_BIN"
push_check head1
echo "approval key (HEAD): $(approval_key "$HEAD_BIN") (v3.13.0 had $(cat "$WORK/approval-pub"))"
echo "store digests vs values: bot=$(vdigest pat2) state=$(vdigest state-repo-token2) secrets-repo=$(vdigest secrets-repo-token)"
digests github-credential-bot/token state-repo-token/token secrets-repo-token/token
echo "CA/key digests in the store vs the v3.4.0 files:"
digests sandbox-access-ca/bundle.pem package-cache-ca/bundle.pem release-approval-key/key.pem 2>&1
for f in sandbox-access-ca.pem package-cache-ca.pem release-approval-key.pem; do echo "$f (v3.4.0 file, trimmed) $(printf '%s' "$(cat "$WORK/pre/$f" 2>/dev/null)" | sha256sum | cut -c1-12)"; done

step "1d. second HEAD start: no deletion logged, only the orphan's warning"
restart "$HEAD_BIN"

step "2b. third HEAD start: the warning repeats"
restart "$HEAD_BIN"

step "2c. follow the warning's re-import instruction: rename the copy back"
stop_daemon
mv "$D/secrets/github/deploy-ssh.ssh.json.migrated" "$D/secrets/github/deploy-ssh.ssh.json"
start "$HEAD_BIN"
"$HEAD_BIN" secrets -data-dir "$D" list | grep deploy-ssh
echo "-- and the next start deletes the new copy:"
restart "$HEAD_BIN"
push_check head2
stop_daemon

step "4. downgrade to v3.13.0 per docs/host-setup.md"
echo "step 2: HEAD once with GRAIN_GITHUB_CREDENTIALS=files"
start "$HEAD_BIN" GRAIN_GITHUB_CREDENTIALS=files
stop_daemon
tree
for f in sandbox-access-ca.pem package-cache-ca.pem release-approval-key.pem; do echo "$f $(digest "$D/$f")"; done | diff - "$WORK/pre-digests.txt" && echo "CA bundles and approval key written back byte-identical to the v3.4.0 originals"
echo "step 3: restore the pre-upgrade backup taken on v3.13.0"
db_restore
echo "step 4: start v3.13.0 (GRAIN_ACCEPT_STORE_BEHIND=1 for its first start)"
if ! start "$V313_BIN" GRAIN_ACCEPT_STORE_BEHIND=1; then
	echo "state-repo/schema-version: $(cat "$D/state-repo/schema-version") (the pre-upgrade copy has $(cat "$WORK/data-after-v313/state-repo/schema-version"))"
	echo "step 3, again: also put back <data-dir>/state-repo from the pre-upgrade copy (docs do not say to)"
	stop_daemon
	mv "$D/state-repo" "$WORK/state-repo-head" && cp -a "$WORK/data-after-v313/state-repo" "$D/state-repo"
	start "$V313_BIN" GRAIN_ACCEPT_STORE_BEHIND=1 || exit 1
fi
push_check downgraded
echo "approval key (v3.13.0 after the downgrade): $(approval_key "$V313_BIN") (before: $(cat "$WORK/approval-pub"))"
stop_daemon

step "3. GRAIN_GITHUB_CREDENTIALS=files on the data dir v3.13.0 left (copies present)"
stop_all
mv "$D" "$WORK/data-main"
cp -a "$WORK/data-after-v313" "$D"
db_restore >/dev/null
start_sims
start "$HEAD_BIN" GRAIN_GITHUB_CREDENTIALS=files
tree
push_check files
echo "-- back to the store: two starts"
restart "$HEAD_BIN"
restart "$HEAD_BIN"
stop_daemon

step "5. hostile copies (store holds every value): $UNFIXED_BIN, then $HEAD_BIN"
mkdir -p "$WORK/outside"
hostile() {
	rm -f "$D/secrets/github/bot.token.migrated" "$D/secrets/state-repo-token.migrated" "$D/package-cache-ca.pem.migrated"
	chmod 700 "$D/secrets/relay" 2>/dev/null
	# A symlink to a file outside the data dir that holds bot's stored
	# value (this data dir is the v3.13.0 one: bot still holds values/pat).
	cp "$WORK/values/pat" "$WORK/outside/bot-token-lookalike"
	cp "$WORK/outside/bot-token-lookalike" "$WORK/outside/lookalike.orig"
	ln -sfn "$WORK/outside/bot-token-lookalike" "$D/secrets/github/bot.token.migrated"
	# A 0644 copy holding the stored state-repo token.
	cp "$WORK/values/state-repo-token" "$D/secrets/state-repo-token.migrated"
	chmod 644 "$D/secrets/state-repo-token.migrated"
	# A FIFO in a CA copy's place.
	mkfifo "$D/package-cache-ca.pem.migrated"
	# An undeletable copy: the relay token's copy in a read-only directory.
	mkdir -p "$D/secrets/relay" && cp "$WORK/values/relay-token" "$D/secrets/relay/token.migrated" && chmod 500 "$D/secrets/relay"
	echo "-- planted:"; migrated
}
for bin in "$UNFIXED_BIN" "$HEAD_BIN"; do
	echo "### $bin"
	hostile
	from=$(($(wc -l <"$WORK/daemon.log") + 1))
	start_daemon "$bin" &
	sp=$!
	for _ in $(seq 30); do kill -0 $sp 2>/dev/null || break; sleep 1; done
	if kill -0 $sp 2>/dev/null; then
		echo "daemon API not up after 30s; last log lines:"; sed -n "$from,\$p" "$WORK/daemon.log" | tail -3 | sed -E 's/^[0-9\/]+ [0-9:.]+ //' | cut -c1-200
		kill $sp 2>/dev/null
	else
		wait $sp && echo "daemon up"
	fi
	sleep 2
	echo "-- find after start:"; migrated
	echo "-- log:"; loglines "$from"
	cmp -s "$WORK/outside/bot-token-lookalike" "$WORK/outside/lookalike.orig" && echo "symlink target intact"
	stop_daemon
	pkill -f "daemon -data-dir $D" 2>/dev/null
done
chmod 700 "$D/secrets/relay"
stop_all
