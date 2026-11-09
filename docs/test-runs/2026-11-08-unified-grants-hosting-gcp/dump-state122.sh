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

# dump-state122.sh <grain.db | postgres://...>: canonical sorted dump of all
# grant, hosting, wait, policy, and note surfaces for diffing SQLite vs
# PostgreSQL and in-place vs prepare-upgrade vs state-repo import.
set -uo pipefail

db=$1
case "$db" in
postgres://*) q() { psql -X -At -F ' | ' "$db" -c "$1"; } ;;
*) q() { sqlite3 -separator ' | ' "$db" "$1"; } ;;
esac

list_col() {
  python3 -c 'import json, sys
for line in sys.stdin.read().splitlines():
    pre, sep, v = line.rpartition(" | ")
    v = v.strip()
    if not v:
        continue
    items = json.loads(v) if v.startswith("[") else [x for x in v.split(",") if x]
    for i in sorted(items):
        print((pre + sep) + i)'
}

echo "# schema"
q "SELECT version FROM grain_schema"

for spec in task_grant:task_id template_grant:template_id schedule_grant:schedule_id; do
  tbl=${spec%%:*}
  col=${spec#*:}
  echo "# $tbl"
  q "SELECT $col, capability, via FROM $tbl WHERE $col NOT LIKE 'system:%' AND $col <> '1' ORDER BY 1, 2"
done

echo "# task_grant_wait"
q "SELECT task_id, capability, resource FROM task_grant_wait ORDER BY 1, 2" 2>/dev/null || true

echo "# hosting"
q "SELECT 'task', id, hosting FROM task WHERE id <> '1' ORDER BY 2" 2>/dev/null || true
q "SELECT 'template', name, hosting FROM template ORDER BY 2" 2>/dev/null || true
q "SELECT 'schedule', id, title, hosting FROM schedule WHERE id NOT LIKE 'system:%' ORDER BY 2" 2>/dev/null || true
q "SELECT 'grain_config', default_hosting FROM grain_config WHERE id = 1" 2>/dev/null || true
q "SELECT 'repo_config', owner||'/'||name, default_hosting FROM repo_config ORDER BY 2" 2>/dev/null || true

echo "# grain_config.default_capabilities"
q "SELECT default_capabilities FROM grain_config WHERE id = 1" | list_col

echo "# repo_config.capabilities"
q "SELECT owner||'/'||name, 'default', default_capabilities FROM repo_config ORDER BY 1" | list_col
q "SELECT owner||'/'||name, 'ci', ci_capabilities FROM repo_config ORDER BY 1" | list_col

tmp=$(mktemp -d)
for col in console_sessions capability_policy; do
  q "SELECT $col FROM grain_config WHERE id = 1" >"$tmp/$col"
done
python3 - "$tmp" <<'PY'
import json, os, sys
d = sys.argv[1]
sessions = json.loads(open(os.path.join(d, "console_sessions")).read().strip() or "[]")
print("# console_sessions")
for s in sorted(sessions, key=lambda x: x.get("ID") or x.get("id") or ""):
    sid = s.get("ID") or s.get("id")
    if sid == "director":
        continue
    caps = sorted(s.get("Capabilities") or s.get("capabilities") or [])
    hosting = s.get("Hosting") or s.get("hosting") or ""
    print(f"{sid} | hosting={hosting} | caps={caps}")
policy = json.loads(open(os.path.join(d, "capability_policy")).read().strip() or "{}")
print("# capability_policy")
print(json.dumps(policy, indent=2, sort_keys=True))
PY
rm -rf "$tmp"

echo "# task_comment notes"
q "SELECT task_id, author_id, body FROM task_comment WHERE task_id <> '1' ORDER BY task_id, id"
echo "# template/schedule notes"
q "SELECT 'template', name, body FROM template WHERE body LIKE '%grain:%' ORDER BY 2" 2>/dev/null || true
q "SELECT 'schedule', id, title, body FROM schedule WHERE body LIKE '%grain:%' ORDER BY 2" 2>/dev/null || true
