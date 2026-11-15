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

# §3 item 3: a state-repository restore (grain/config imported into an empty
# PostgreSQL store, staterepo.Load -> ImportTables) and a settings merge of a
# commit to grain/config (applySettings -> importInto with a merge plan) that
# brings rows with explicit ids above every sequence, then writes and a
# nextval probe across every serial-id table.
source "$(dirname "$0")/env.sh"
reset_db
reset_work

echo "== HEAD populates a deployment"
start_daemon "$GRAIN"
for i in 1 2 3 4; do writes "orig-$i"; done
api POST /api/boards '{"name":"walk board"}' | cut -c1-4
stop_daemon
GRAIN_SANDBOX_CLI=0 "$GRAIN" state -data-dir "$WORK_DIR/data" sync 2>&1 | tail -1
git -C "$WORK_DIR/data/state-repo" log -1 --oneline
echo "-- sequences before the restore"
seq_summary

echo "== restore: a rebuilt host -- a fresh data directory and an empty database, holding only"
echo "   a clone of grain/config -- loads the repository into the store (staterepo.Load -> ImportTables)"
RESTORED=${WORK_DIR}-restored
rm -rf "$RESTORED"
mkdir -p "$RESTORED/data"
git clone -q "$WORK_DIR/data/state-repo" "$RESTORED/data/state-repo"
reset_db
: >"$WORK_DIR/daemon.log"
DATA_DIR=$RESTORED/data start_daemon "$GRAIN"
grep -E "staterepo|sequence" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 | sort -u || true
echo "-- sequences after the restore (before writes)"
seq_summary
writes restored-1
api POST /api/boards '{"name":"restored board"}' | cut -c1-4
echo "-- sequences after writes"
seq_summary
nextval_probe

echo "== settings merge: a commit to grain/config adds template-50 and suite-50 with explicit ids"
repo="$RESTORED/data/state-repo"
python3 - "$repo" <<'PY'
import json, sys, re
repo = sys.argv[1]
t = open(f"{repo}/settings/templates.yml").read()
# Every key the export writes: a minimal entry is refused (SQLSTATE 23502,
# template.target_owner NULL), recorded separately in the report.
first = t[t.index("  - id: "):]
first = first[:first.index("\n  - id: ", 1) + 1] if "\n  - id: " in first[1:] else first
entry = re.sub(r"id: template-\d+", "id: template-50", first, count=1)
entry = re.sub(r"name: .*", "name: merged-50", entry, count=1)
t += entry if entry.endswith("\n") else entry + "\n"
open(f"{repo}/settings/templates.yml", "w").write(t)
s = open(f"{repo}/settings/suites.yml").read()
s += '''  - id: suite-50
    name: merged suite
    templates:
      - template-50
    mode: count
    count: 1
    max-passes: 0
    require-approval: false
    auto-merge: false
    auto-approve-proposals: false
    created-at: "2026-11-15T12:00:00Z"
'''
open(f"{repo}/settings/suites.yml", "w").write(s)
for table, col in (("template_sequence", "number"), ("suite_sequence", "id")):
    p = f"{repo}/tables/{table}.json"
    rows = json.load(open(p))
    rows.append({col: 50, "issued_at": "2026-11-15T12:00:00Z"})
    json.dump(rows, open(p, "w"), indent=2)
PY
git -C "$repo" -c user.name="Walk" -c user.email="walk@example.com" commit -qam "walk: add template-50 and suite-50"
echo "-- waiting for the running daemon to merge the commit"
for _ in $(seq 60); do
	[[ $(psql_db -c "SELECT count(*) FROM template WHERE id = 'template-50'") == 1 ]] && break
	sleep 1
done
psql_db -c "SELECT 'template-50 present: ' || count(*) FROM template WHERE id = 'template-50'"
grep -E "sequence|merg|applied|settings" "$WORK_DIR/daemon.log" | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-200 | tail -5 || true
echo "-- sequences after the merge (before writes)"
seq_summary
writes merged-1
writes merged-2
echo "-- sequences after writes"
seq_summary
nextval_probe
stop_daemon
