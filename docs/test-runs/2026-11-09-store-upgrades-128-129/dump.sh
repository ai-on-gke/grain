#!/bin/bash
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

# dump.sh <grain.db | postgres://...>: every surface that holds a
# grant, one line per grant, sorted, so two dumps diff cleanly. JSON
# columns are pretty-printed with sorted keys; list columns (comma lists
# at 93, JSON arrays later) one entry per line; secrets are redacted.
db=$1
case $db in
postgres://*) q() { psql -X -At -F ' | ' "$db" -c "$1"; } ;;
*) q() { sqlite3 -separator ' | ' "$db" "$1"; } ;;
esac
list() { python3 -c 'import json,sys
for line in sys.stdin.read().splitlines():
    pre, sep, v = line.rpartition(" | ")
    v = v.strip()
    items = json.loads(v) if v.startswith("[") else [x for x in v.split(",") if x]
    for i in items: print((pre + sep) + i)'; }
echo "# schema"; q "SELECT version FROM grain_schema"
for t in task_grant:task_id template_grant:template_id schedule_grant:schedule_id; do
  echo "# ${t%%:*}"
  q "SELECT ${t#*:}, capability, via FROM ${t%%:*} WHERE ${t#*:} NOT LIKE 'system:%' AND ${t#*:} <> '1' ORDER BY 1, 2"
done
echo "# grain_config.default_capabilities"; q "SELECT default_capabilities FROM grain_config" | list
echo "# repo_config"
q "SELECT owner||'/'||name, 'default', default_capabilities FROM repo_config" | list
q "SELECT owner||'/'||name, 'ci', ci_capabilities FROM repo_config" | list
tmp=$(mktemp -d)
for col in console_sessions capability_policy mcp_servers agent_backends; do
  q "SELECT $col FROM grain_config WHERE id=1" > "$tmp/$col"
done
python3 - "$tmp" <<'PY'
import json, os, re, sys
row = [open(os.path.join(sys.argv[1], c)).read().strip() for c in ("console_sessions", "capability_policy", "mcp_servers", "agent_backends")]
def red(o):
    if isinstance(o, dict):
        return {k: ("<redacted>" if re.search(r"(token|secret|key_json|password)$", k, re.I) and isinstance(v, str) and v else red(v)) for k, v in o.items()}
    if isinstance(o, list):
        return [red(x) for x in o]
    return o
print("# console_sessions (id: capabilities)")
for s in json.loads(row[0] or "[]"):
    sid = s.get("ID") or s.get("id")
    if sid in ("director",):
        continue
    print(sid + ":", sorted(s.get("Capabilities") or s.get("capabilities") or []))
print("# capability_policy")
print(json.dumps(red(json.loads(row[1] or "{}")), indent=1, sort_keys=True))
print("# mcp_servers")
print(json.dumps(red(json.loads(row[2] or "{}")), indent=1, sort_keys=True))
print("# agent_backends")
bs = json.loads(row[3] or "[]")
for b in bs:
    b.pop("discovered", None)
print(json.dumps(red(bs), indent=1, sort_keys=True))
PY
rm -rf "$tmp"
echo "# admission (absent before schema 122)"
q "SELECT 'task', id, admission FROM task WHERE id <> '1' ORDER BY 1, 2" 2>/dev/null
q "SELECT 'template', id, admission FROM template ORDER BY 2" 2>/dev/null
q "SELECT 'schedule', id, admission FROM schedule WHERE id NOT LIKE 'system:%' ORDER BY 2" 2>/dev/null
q "SELECT 'grain_config', default_admission FROM grain_config" 2>/dev/null
q "SELECT 'repo_config', owner||'/'||name, default_admission FROM repo_config ORDER BY 2" 2>/dev/null
echo "# task notes (task_comment by grain)"
q "SELECT task_id, author_id, body FROM task_comment WHERE task_id <> '1' ORDER BY task_id, id"
echo "# template/schedule notes (bodies mentioning grain:)"
q "SELECT 'template', id, body FROM template WHERE body LIKE '%grain:%'" 2>/dev/null
q "SELECT 'schedule', id, body FROM schedule WHERE body LIKE '%grain:%'" 2>/dev/null
echo "# task_grant_wait (task, capability, resource)"
q "SELECT task_id, capability, resource FROM task_grant_wait ORDER BY 1"
