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

# §1 item 1: a pre-bcd2b5a83 schema-146 store (tasks stored, no
# task_source_template) opened by this build, then `grain state check` and a
# state-repo export -> external settings repo edit -> import round-trip.
# STORE=sqlite|postgres.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
STORE=${STORE:-sqlite}
WORK_DIR="/tmp/walk-st-item1-${STORE}"
trap 'stop_env "$WORK_DIR"' EXIT

table_count() {
	if [[ $STORE == postgres ]]; then
		sql_query "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'task_source_template';"
	else
		sql_query "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'task_source_template';"
	fi
}
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "=== Item 1 ($STORE): seeding a store ==="
init_env "$WORK_DIR"
start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"
TMPL_ID=$(api POST /api/templates '{"name":"legacy-audit","title":"Legacy audit","description":"Audit pre-existing tasks."}' | jq -r .id)
T_SCRATCH=$(api POST /api/tasks '{"title":"Pre-existing scratch task","description":"no template","noRepo":true}' | jq -r .id)
T_LEGACY=$(api POST /api/tasks "{\"templateId\":\"$TMPL_ID\",\"noRepo\":true}" | jq -r .id)
echo "template=$TMPL_ID scratch=$T_SCRATCH from_template=$T_LEGACY"
stop_env "$WORK_DIR"

VER=$(sql_query "SELECT version FROM grain_schema WHERE id = 1;")
echo "schema version: $VER"
[[ $VER == 146 ]] || fail "expected schema 146"
# What a pre-bcd2b5a83 schema-146 store looks like: the same rows, no table.
sql_query "DROP TABLE task_source_template;"
rm -rf "$WORK_DIR/data/state-repo"
echo "task_source_template present after drop: $(table_count)"

echo "=== Item 1 ($STORE): this build's daemon on the old store ==="
start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"
echo "after Init: task_source_template present=$(table_count) schema=$(sql_query 'SELECT version FROM grain_schema WHERE id = 1;')"
[[ $(table_count) == 1 ]] || fail "Init did not create task_source_template"

LIST=$(api GET "/api/tasks?fields=full")
for id in "$T_SCRATCH" "$T_LEGACY"; do
	echo "task $id: list=$(echo "$LIST" | jq -c --arg id "$id" '.[] | select(.id == $id) | {templateId}') detail=$(api GET "/api/tasks/$id" | jq -c '(.task // .) | {templateId}')"
done
[[ $(echo "$LIST" | jq --arg id "$T_SCRATCH" '.[] | select(.id == $id) | has("templateId")') == false ]] || fail "scratch task shows templateId"
[[ $(api GET "/api/tasks/$T_SCRATCH" | jq '(.task // .) | has("templateId")') == false ]] || fail "scratch detail shows templateId"
# task_filed_template survived, and hydrate falls back to it for the report.
[[ $(api GET "/api/tasks/$T_LEGACY" | jq -r '(.task // .).templateId') == "$TMPL_ID" ]] || fail "pre-existing template task lost templateId"

# A proposal naming the template: provenance only, task_filed_template empty.
T_PROP=$(api POST /api/tasks "{\"templateId\":\"$TMPL_ID\",\"noRepo\":true,\"proposedBy\":\"$T_SCRATCH\"}" | jq -r .id)
echo "proposal $T_PROP: task_filed_template rows=$(sql_query "SELECT count(*) FROM task_filed_template WHERE task_id = '$T_PROP';") task_source_template=$(sql_query "SELECT template_id FROM task_source_template WHERE task_id = '$T_PROP';")"
[[ $(sql_query "SELECT count(*) FROM task_filed_template WHERE task_id = '$T_PROP';") == 0 ]] || fail "proposal got TemplateID"
stop_env "$WORK_DIR"

echo "=== Item 1 ($STORE): state-repo export, grain state check, round-trip ==="
rm -f "$WORK_DIR/data/state-repo.json"
"$GRAIN" state -data-dir "$WORK_DIR/data" sync
"$GRAIN" state check "$WORK_DIR/data/state-repo"
if find "$WORK_DIR/data/state-repo" -name '*source_template*' -not -path '*/.git/*' | grep -q . ||
	grep -rq task_source_template "$WORK_DIR/data/state-repo/settings" "$WORK_DIR/data/state-repo/tables"; then
	fail "task_source_template exported into the state repository"
fi
echo "verified: the state repository carries no task_source_template"

EXT_BARE="$WORK_DIR/ext-settings.git"
git init -q --bare -b main "$EXT_BARE"
EXT=$(mktemp -d)
git clone -q "$WORK_DIR/data/state-repo" "$EXT"
git -C "$EXT" push -q "$EXT_BARE" HEAD:main
"$GRAIN" state -data-dir "$WORK_DIR/data" repo "$EXT_BARE"
cat >>"$EXT/settings/templates.yml" <<'EOF'
  - id: ext-added
    name: ext-added
    title: Added from external settings repo
    body: Imported by grain state sync.
    base: ""
    auto-merge: false
    network: ""
    nothing-needed: ""
    model: ""
    goal: ""
    interactive: false
    keep-running: false
    admission: ""
    hosting: ""
    skills: ""
    sandbox: ""
    target: ""
    change-target: ""
    reviewers: ""
    priority: 0
    created-at: "2026-11-14T09:50:00Z"
EOF
git -C "$EXT" -c user.name=Ops -c user.email=ops@example.com commit -qam "feat: add ext-added template"
git -C "$EXT" push -q "$EXT_BARE" HEAD:main
"$GRAIN" state check "$EXT"
"$GRAIN" state -data-dir "$WORK_DIR/data" sync

start_ghsim "$WORK_DIR"
start_daemon "$GRAIN" "$WORK_DIR"
P_LIST=$(api GET "/api/tasks?fields=full" | jq -r --arg id "$T_PROP" '.[] | select(.id == $id) | .templateId // "<omitted>"')
P_DETAIL=$(api GET "/api/tasks/$T_PROP" | jq -r '(.task // .).templateId // "<omitted>"')
FILED=$(sql_query "SELECT count(*) FROM task_filed_template WHERE task_id = '$T_PROP';")
SOURCE=$(sql_query "SELECT template_id FROM task_source_template WHERE task_id = '$T_PROP';")
EXT_CNT=$(sql_query "SELECT count(*) FROM template WHERE id = 'ext-added';")
echo "after round-trip: proposal list=$P_LIST detail=$P_DETAIL filed_rows=$FILED source=$SOURCE ext-added=$EXT_CNT"
[[ $P_LIST == "$TMPL_ID" && $P_DETAIL == "$TMPL_ID" && $FILED == 0 && $SOURCE == "$TMPL_ID" && $EXT_CNT == 1 ]] || fail "round-trip lost provenance or the settings edit"
rm -rf "$EXT"
echo "PASS: Item 1 ($STORE)"
