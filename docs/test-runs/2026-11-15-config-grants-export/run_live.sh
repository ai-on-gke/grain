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

# STORE=sqlite|postgres run_live.sh: §6 items 2-3 on real daemons.
# Daemon A seeds the templates and schedule and exports them into its
# grain/config, which is pushed to a local bare remote; daemon B, on a
# fresh store, imports that remote in read-only IaC mode, and a task
# filed from each template on B runs probe.sh through the fake agent.
set -euo pipefail
GRAIN=${GRAIN:-/home/debian/bin/grain-fix}
source "$(dirname "$0")/lib.sh"
W=/tmp/walk-live-${STORE:-sqlite}
out=$W/out
init_env "$W"
mkdir -p "$out"
trap 'stop_env "$W"; [[ -n ${B:-} ]] && stop_daemon "$B" || true' EXIT
for n in docs gadgets; do
	git init -q --bare -b main "$W/$n.git"
	git -C "$W/$n.git" config http.receivepack true
	t=$(mktemp -d)
	git -C "$t" init -q -b main
	echo "# acme/$n" >"$t/README.md"
	git -C "$t" add . && git -C "$t" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed $n"
	git -C "$t" push -q "$W/$n.git" main main:release
	rm -rf "$t"
done
(cd "$ROOT" && go build -o "$W/bin/ghsim" "$HERE15/ghsim.go")
"$W/bin/ghsim" -addr "$GHSIM" -root "$W" >"$W/ghsim.log" 2>&1 &
echo $! >"$W/ghsim.pid"
for _ in $(seq 50); do curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && break; sleep 0.1; done

# A: seed and export.
start_daemon "$W"
seed_access >"$out/seed.json"
sleep 3
dump_access >"$out/A-tables.txt"
stop_daemon "$W"
git -C "$W/data/state-repo" log --oneline >"$out/A-log.txt"
cp "$W/data/state-repo/settings/templates.yml" "$W/data/state-repo/settings/schedules.yml" "$out/"
git clone -q --bare "$W/data/state-repo" "$W/config.git"

# B: fresh store (a second PostgreSQL database, or a new SQLite file),
# importing the bare remote in IaC mode.
B=$W/b
mkdir -p "$B/data" "$B/sandboxes" "$B/home" "$B/runs"
cp "$W/gemini.key" "$W/seed-models.yaml" "$B/"
if [[ ${STORE:-sqlite} == postgres ]]; then
	sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk WITH (FORCE);"
	sudo -u postgres psql -q -c "CREATE DATABASE grain_walk OWNER grain;"
	"$GRAIN" connections -data-dir "$B/data" -db-driver postgres -db-dsn "$PG_DSN" import "$W/seed-git.yaml" >/dev/null
else
	"$GRAIN" connections -data-dir "$B/data" import "$W/seed-git.yaml" >/dev/null
fi
"$GRAIN" state -data-dir "$B/data" repo -branch main "file://$W/config.git" >"$out/B-repo.txt"
start_daemon "$B"
sleep 3
dump_access "$B/data/store/grain.db" >"$out/B-tables.txt"
api GET /api/state-repo | jq -c '{mode, head, error}' >"$out/B-state.json"
api GET /api/templates >"$out/B-templates.json"
api GET /api/schedules >"$out/B-schedules.json"
diff <(grep -v '^system:' "$out/A-tables.txt") <(grep -v '^system:' "$out/B-tables.txt") >"$out/A-B.diff" && echo "A and B tables identical" >>"$out/A-B.diff" || true

# Dispatch one task from each template; the fake agent runs probe.sh.
cp "$HERE15/probe.sh" "$B/probe.sh"
cat >"$B/action-default.json" <<JSON
{"kind":"mcp_sequence","calls":[
 {"tool":"run_command","args":{"command":"bash $B/probe.sh","description":"probing repository access"}},
 {"tool":"comment_on_issue","args":{"comment":"probe done"}}]}
JSON
for tpl in template-1 template-2 template-3; do
	curl -sS -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d "{\"templateId\":\"$tpl\",\"approved\":true}" "$DAEMON/api/tasks" |
		jq -c '{id, title, reads, writes, state, error}' >>"$out/B-tasks.jsonl"
done
for _ in $(seq 120); do
	[[ $(ls "$B/runs"/mcp-*.jsonl 2>/dev/null | wc -l) -ge 2 ]] && break
	sleep 1
done
for f in "$B/runs"/mcp-*.jsonl; do
	echo "== $(basename "$f")"
	jq -r '.outputs[0].text' "$f"
done >"$out/B-probes.txt"
for n in docs gadgets; do
	echo "== acme/$n"; git -C "$W/$n.git" for-each-ref --format='%(refname:short)' refs/heads
done >"$out/B-remote-branches.txt"
