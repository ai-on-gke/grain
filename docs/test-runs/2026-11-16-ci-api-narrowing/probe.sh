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

# probe.sh: probes a narrowed CI API token (GRAIN_API_URL + GRAIN_API_TOKEN)
# minted for a non-grain repository listed in grain_config.ci_api_repos
# (default acme/widgets) against every route family, emitting a TSV/markdown
# route -> status -> note table and exiting non-zero if any security leak or
# unexpected status is observed.
#
# Required env:
#   GRAIN_API_URL          CI API listener URL
#   GRAIN_API_TOKEN        narrowed CI API bearer token for OWN_REPO
#
# Optional fixture env (enables deeper cross-repo checks when provided):
#   OWN_REPO               default: acme/widgets
#   FOREIGN_REPO           default: acme/secret-other
#   OWN_TASK_ID            task in OWN_REPO
#   FOREIGN_TASK_ID        task in FOREIGN_REPO
#   NOREPO_TASK_ID         standalone (no-repo) task
#   OWN_ATTACH_ID          attachment id on OWN_TASK_ID
#   FOREIGN_ATTACH_ID      attachment id on FOREIGN_TASK_ID
#   OWN_RUN_ID             grain CI run id in OWN_REPO
#   FOREIGN_RUN_ID         grain CI run id in FOREIGN_REPO
#   FOREIGN_GHA_RUN_ID     e.g. gha:acme/secret-other:9001
#   OWN_ARTIFACT_PATH      <job>/<name> on OWN_RUN_ID
#   FOREIGN_ARTIFACT_PATH  <job>/<name> on FOREIGN_RUN_ID
#   FOREIGN_SCHED_ID       schedule id in FOREIGN_REPO
#   FOREIGN_SECRET_WORD    unique substring present in FOREIGN_REPO fixtures
#   FOREIGN_SANDBOX_NAME   persistent sandbox name in FOREIGN_REPO
#   FOREIGN_TEMPLATE_ID    template id in FOREIGN_REPO
#   FOREIGN_SUITE_RUN_ID   suite run id in FOREIGN_REPO
#   EXT_REF                shared externalRef present on both own and foreign tasks
#   RELEASE_GATE_TOKEN     optional token holding api:release-gate-read for OWN_REPO
#   OUT_JSON               optional path to write detailed JSON probe log

set -euo pipefail

: "${GRAIN_API_URL:?GRAIN_API_URL is required}"
: "${GRAIN_API_TOKEN:?GRAIN_API_TOKEN is required}"

OWN_REPO=${OWN_REPO:-acme/widgets}
FOREIGN_REPO=${FOREIGN_REPO:-acme/secret-other}
OWN_GHA_RUN_ID=${OWN_GHA_RUN_ID:-gha:acme:widgets:9002}
FOREIGN_GHA_RUN_ID=${FOREIGN_GHA_RUN_ID:-gha:acme:secret-other:9001}
FOREIGN_SECRET_WORD=${FOREIGN_SECRET_WORD:-cobalt-zebra-secret}
FOREIGN_SANDBOX_NAME=${FOREIGN_SANDBOX_NAME:-alpha-other-secret-svc}
EXT_REF=${EXT_REF:-WALK-42}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

FAILS=0
RESULTS_FILE="$TMP/results.jsonl"
: >"$RESULTS_FILE"

printf '| %-6s | %-62s | %-6s | %-8s | %s |\n' 'Method' 'Route' 'Status' 'Result' 'Notes'
printf '|--------|----------------------------------------------------------------|--------|----------|-------|\n'

# record method path status ok_flag note [body_preview]
record() {
  local method=$1 path=$2 status=$3 res=$4 note=$5
  printf '| %-6s | %-62s | %-6s | %-8s | %s |\n' "$method" "$path" "$status" "$res" "$note"
  jq -cn --arg m "$method" --arg p "$path" --arg s "$status" --arg r "$res" --arg n "$note" \
    '{method:$m, path:$p, status:($s|tonumber), result:$r, note:$n}' >>"$RESULTS_FILE"
  if [[ "$res" == "LEAK" || "$res" == "FAIL" ]]; then
    FAILS=$((FAILS + 1))
  fi
}

# call_api token method path [body] -> prints status, writes body to $TMP/body
call_api() {
  local tok=$1 method=$2 path=$3 body=${4:-}
  local args=(-sS -o "$TMP/body" -w '%{http_code}' -X "$method")
  if [[ -n "$tok" ]]; then
    args+=(-H "Authorization: Bearer $tok")
  fi
  if [[ -n "$body" ]]; then
    args+=(-H "Content-Type: application/json" -d "$body")
  fi
  curl "${args[@]}" "${GRAIN_API_URL}${path}"
}

# expect_denied method path [body] [token]
expect_denied() {
  local method=$1 path=$2 body=${3:-} tok=${4:-$GRAIN_API_TOKEN}
  local code msg
  code=$(call_api "$tok" "$method" "$path" "$body")
  msg=$(jq -r '.error // empty' "$TMP/body" 2>/dev/null || head -c 120 "$TMP/body")
  msg=${msg//$'\n'/ }
  if [[ "$code" == "401" || "$code" == "403" || "$code" == "404" ]]; then
    record "$method" "$path" "$code" "PASS" "${msg:0:90}"
  else
    record "$method" "$path" "$code" "LEAK" "expected 403/404, got $code: $(head -c 140 "$TMP/body" | tr '\n' ' ')"
  fi
}

# expect_narrowed method path [note_expr] [check_no_foreign_word]
expect_narrowed() {
  local method=$1 path=$2 note_expr=${3:-} check_secret=${4:-1}
  local code body_txt note leak=""
  code=$(call_api "$GRAIN_API_TOKEN" "$method" "$path")
  body_txt=$(cat "$TMP/body")
  if [[ "$code" != "200" ]]; then
    record "$method" "$path" "$code" "FAIL" "expected 200, got $code: $(printf '%s' "$body_txt" | head -c 120 | tr '\n' ' ')"
    return
  fi
  if [[ "$check_secret" == "1" ]]; then
    if printf '%s' "$body_txt" | grep -qiF "$FOREIGN_REPO"; then
      leak="body mentions $FOREIGN_REPO"
    elif [[ -n "$FOREIGN_SECRET_WORD" ]] && printf '%s' "$body_txt" | grep -qiF "$FOREIGN_SECRET_WORD"; then
      leak="body mentions $FOREIGN_SECRET_WORD"
    elif [[ -n "${FOREIGN_TASK_ID:-}" ]] && printf '%s' "$body_txt" | grep -qE "\"id\"[[:space:]]*:[[:space:]]*\"${FOREIGN_TASK_ID}\""; then
      leak="body contains foreign task $FOREIGN_TASK_ID"
    elif [[ -n "${NOREPO_TASK_ID:-}" ]] && printf '%s' "$body_txt" | grep -qE "\"id\"[[:space:]]*:[[:space:]]*\"${NOREPO_TASK_ID}\""; then
      leak="body contains no-repo task $NOREPO_TASK_ID"
    fi
  fi
  if [[ -n "$note_expr" ]]; then
    note=$(jq -r "$note_expr" "$TMP/body" 2>/dev/null || echo "non-json (${#body_txt}B)")
  else
    note="${#body_txt} bytes"
  fi
  if [[ -n "$leak" ]]; then
    record "$method" "$path" "$code" "LEAK" "$leak ($note)"
  else
    record "$method" "$path" "$code" "PASS" "$note"
  fi
}

# 1. Auth boundary
code=$(call_api "" GET "/api/tasks")
if [[ "$code" == "401" ]]; then
  record GET "/api/tasks (no token)" "$code" "PASS" "unauthorized without bearer token"
else
  record GET "/api/tasks (no token)" "$code" "FAIL" "expected 401"
fi
code=$(call_api "bogus-token" GET "/api/tasks")
if [[ "$code" == "401" ]]; then
  record GET "/api/tasks (bad token)" "$code" "PASS" "unauthorized with bogus token"
else
  record GET "/api/tasks (bad token)" "$code" "FAIL" "expected 401"
fi

# 2. Tasks & summaries & task sub-routes
expect_narrowed GET "/api/tasks" '"count=" + (length|tostring) + " repos=" + ([.[].repo]|unique|join(","))'
expect_denied   GET "/api/tasks?repo=${FOREIGN_REPO}"
expect_denied   GET "/api/tasks/states"
expect_denied   GET "/api/states"

if [[ -n "${OWN_TASK_ID:-}" && -n "${FOREIGN_TASK_ID:-}" && -n "${NOREPO_TASK_ID:-}" ]]; then
  expect_narrowed GET "/api/tasks/summaries?ids=${OWN_TASK_ID},${FOREIGN_TASK_ID},${NOREPO_TASK_ID}" \
    '"ids=" + ([.tasks[].id]|join(",")) + " missing=" + ([.missing[]?]|join(","))'
  expect_denied   GET "/api/tasks/summaries?ids=${OWN_TASK_ID}&repo=${FOREIGN_REPO}"
  expect_narrowed GET "/api/tasks/wait?ids=${OWN_TASK_ID}&until=any&timeout=1" '"tasks=" + ([.tasks[].id]|join(","))'
  expect_denied   GET "/api/tasks/wait?ids=${FOREIGN_TASK_ID}&until=any&timeout=1"
  expect_denied   GET "/api/tasks/wait?ids=${NOREPO_TASK_ID}&until=any&timeout=1"
  expect_denied   GET "/api/tasks/wait?ids=${OWN_TASK_ID},${FOREIGN_TASK_ID}&until=any&timeout=1"

  expect_narrowed GET "/api/tasks/${OWN_TASK_ID}" '"id=" + .id + " repo=" + .repo'
  expect_denied   GET "/api/tasks/${FOREIGN_TASK_ID}"
  expect_denied   GET "/api/tasks/${NOREPO_TASK_ID}"

  for sub in messages prompt context diff-preview tree attempts/1/transcript attempts/1/capability-audit; do
    expect_denied GET "/api/tasks/${FOREIGN_TASK_ID}/${sub}"
    expect_denied GET "/api/tasks/${NOREPO_TASK_ID}/${sub}"
  done
  expect_narrowed GET "/api/tasks/${OWN_TASK_ID}/messages" '"messages=" + (if type=="array" then length else (.messages|length) end | tostring)'
  expect_narrowed GET "/api/tasks/${OWN_TASK_ID}/prompt" '"prompt ok"' 1
  expect_narrowed GET "/api/tasks/${OWN_TASK_ID}/context" '"context ok"' 1
  if [[ -n "${FOREIGN_ATTACH_ID:-}" ]]; then
    expect_denied GET "/api/tasks/${FOREIGN_TASK_ID}/attachments/${FOREIGN_ATTACH_ID}"
  fi
  if [[ -n "${OWN_ATTACH_ID:-}" ]]; then
    expect_narrowed GET "/api/tasks/${OWN_TASK_ID}/attachments/${OWN_ATTACH_ID}" '"attachment bytes"' 1
  fi
fi

expect_narrowed GET "/api/inbox" '"items=" + (.items|length|tostring)'
expect_denied   GET "/api/inbox?repo=${FOREIGN_REPO}"
expect_narrowed GET "/api/refs/${EXT_REF}/summary" '"total=" + (.total|tostring) + " ids=" + ([.tasks[]?.id]|join(","))'
expect_denied   GET "/api/refs/${EXT_REF}/summary?repo=${FOREIGN_REPO}"

# 3. Namespace /api/fs/*
expect_denied   GET "/api/fs/task/"
if [[ -n "${OWN_TASK_ID:-}" ]]; then
  expect_narrowed GET "/api/fs/task/${OWN_TASK_ID}/summary.md" '"summary.md readable"' 1
fi
if [[ -n "${FOREIGN_TASK_ID:-}" ]]; then
  expect_denied GET "/api/fs/task/${FOREIGN_TASK_ID}/summary.md"
fi
if [[ -n "${NOREPO_TASK_ID:-}" ]]; then
  expect_denied GET "/api/fs/task/${NOREPO_TASK_ID}/summary.md"
fi
expect_narrowed GET "/api/fs/ci/" '"entries=" + ([.entries[]?.name]|join(","))'
expect_narrowed GET "/api/fs/ci/${OWN_REPO}" '"entries=" + ([.entries[]?.name]|length|tostring)'
expect_denied   GET "/api/fs/ci/${FOREIGN_REPO}"
if [[ -n "${FOREIGN_RUN_ID:-}" ]]; then
  expect_denied GET "/api/fs/ci/${FOREIGN_REPO}/${FOREIGN_RUN_ID}"
fi
expect_denied GET "/api/fs/src/${OWN_REPO}"
expect_denied GET "/api/fs/config"

# 4. CI runs, logs, artifacts, workflows, faults, merge-batches, base-branch-gates
expect_narrowed GET "/api/ci/runs" '"runs=" + ((.runs // .)|length|tostring)'
expect_denied   GET "/api/ci/runs?repo=${FOREIGN_REPO}"
if [[ -n "${OWN_RUN_ID:-}" ]]; then
  expect_narrowed GET "/api/ci/runs/${OWN_RUN_ID}" '"id=" + .id + " repo=" + .repo'
  expect_narrowed GET "/api/ci/runs/${OWN_RUN_ID}/artifacts" '"artifacts=" + ((.artifacts // .items // [])|length|tostring)'
  if [[ -n "${OWN_ARTIFACT_PATH:-}" ]]; then
    expect_narrowed GET "/api/ci/runs/${OWN_RUN_ID}/artifacts/${OWN_ARTIFACT_PATH}" '"artifact readable"' 1
  fi
fi
expect_narrowed GET "/api/ci/runs/${OWN_GHA_RUN_ID}" '"gha own run ok"' 1
expect_narrowed GET "/api/ci/runs/${OWN_GHA_RUN_ID}/log" '"gha own log ok"' 1
if [[ -n "${FOREIGN_RUN_ID:-}" ]]; then
  expect_denied GET "/api/ci/runs/${FOREIGN_RUN_ID}"
  expect_denied GET "/api/ci/runs/${FOREIGN_RUN_ID}/log"
  expect_denied GET "/api/ci/runs/${FOREIGN_RUN_ID}/artifacts"
  if [[ -n "${FOREIGN_ARTIFACT_PATH:-}" ]]; then
    expect_denied GET "/api/ci/runs/${FOREIGN_RUN_ID}/artifacts/${FOREIGN_ARTIFACT_PATH}"
  fi
fi
expect_denied   GET "/api/ci/runs/${FOREIGN_GHA_RUN_ID}"
expect_denied   GET "/api/ci/runs/${FOREIGN_GHA_RUN_ID}/log"
expect_denied   GET "/api/ci/workflows?ref=main"
expect_denied   GET "/api/ci/workflows?repo=${FOREIGN_REPO}&ref=main"
expect_narrowed GET "/api/ci/faults" '"faults=" + ((.faults // [])|length|tostring)'
expect_denied   GET "/api/ci/faults?repo=${FOREIGN_REPO}"
expect_narrowed GET "/api/merge-batches" '"batches=" + ((.batches // [])|length|tostring)'
expect_denied   GET "/api/merge-batches?repo=${FOREIGN_REPO}"
expect_narrowed GET "/api/base-branch-gates" '"gates=" + ((.gates // [])|length|tostring)'
expect_denied   GET "/api/base-branch-gates?repo=${FOREIGN_REPO}"
expect_denied   GET "/api/pull-requests/status?pr=${FOREIGN_REPO}%231"

# 5. Events, pipeline changes, notifications
code=$(call_api "$GRAIN_API_TOKEN" GET "/api/events?since=0&limit=100")
if [[ "$code" == "200" ]]; then
  ev_count=$(jq -r '.events | length' "$TMP/body" 2>/dev/null || echo err)
  ev_bad=$(jq -r --arg own "$OWN_REPO" --arg ft "${FOREIGN_TASK_ID:-}" --arg nt "${NOREPO_TASK_ID:-}" \
    '[.events[] | select((.repo // "") != $own or (.taskId // "") == $ft or (.taskId // "") == $nt)] | length' "$TMP/body" 2>/dev/null || echo err)
  if [[ "$ev_bad" == "0" ]] && ! grep -qiF "$FOREIGN_SECRET_WORD" "$TMP/body"; then
    record GET "/api/events?since=0&limit=100" "$code" "PASS" "events=${ev_count} (all repo=${OWN_REPO})"
  else
    record GET "/api/events?since=0&limit=100" "$code" "LEAK" "events=${ev_count} foreign_or_norepo=${ev_bad}"
  fi
else
  record GET "/api/events?since=0&limit=100" "$code" "FAIL" "expected 200"
fi
expect_denied   GET "/api/events?since=0&repo=${FOREIGN_REPO}"
expect_narrowed GET "/api/pipeline/changes" '"entries=" + ((.entries // [])|length|tostring)'
expect_denied   GET "/api/pipeline/changes?repo=${FOREIGN_REPO}"
expect_narrowed GET "/api/notifications" '"count=" + (length|tostring)'
expect_narrowed GET "/api/notifications?limit=1&q=${FOREIGN_SECRET_WORD}" '"total=" + (.total|tostring) + " items=" + (.items|length|tostring)'
# Check total==0 for foreign secret query on notifications
notif_total=$(jq -r '.total // 0' "$TMP/body" 2>/dev/null || echo 0)
if [[ "$notif_total" != "0" ]]; then
  record GET "/api/notifications?limit=1&q=${FOREIGN_SECRET_WORD} (oracle)" "200" "LEAK" "total=${notif_total} leaked foreign notification match"
fi

# 6. Schedules, persistent sandboxes, templates, suites (including paged & ?q= oracle checks!)
expect_narrowed GET "/api/schedules" '"count=" + (length|tostring)'
code=$(call_api "$GRAIN_API_TOKEN" GET "/api/schedules?limit=1&q=${FOREIGN_SECRET_WORD}")
sched_total=$(jq -r '.total // 0' "$TMP/body" 2>/dev/null || echo "err")
sched_items=$(jq -r '.items | length' "$TMP/body" 2>/dev/null || echo "err")
if [[ "$code" == "200" && "$sched_total" == "0" && "$sched_items" == "0" ]]; then
  record GET "/api/schedules?limit=1&q=${FOREIGN_SECRET_WORD}" "$code" "PASS" "total=0 items=0 (no cross-repo oracle)"
else
  record GET "/api/schedules?limit=1&q=${FOREIGN_SECRET_WORD}" "$code" "LEAK" "total=${sched_total} items=${sched_items} (cross-repo schedule count/oracle leak!)"
fi
code=$(call_api "$GRAIN_API_TOKEN" GET "/api/schedules?limit=1&f=repo:any:${FOREIGN_REPO}")
sched_ftotal=$(jq -r '.total // 0' "$TMP/body" 2>/dev/null || echo "err")
if [[ "$code" == "200" && "$sched_ftotal" == "0" ]]; then
  record GET "/api/schedules?limit=1&f=repo:any:${FOREIGN_REPO}" "$code" "PASS" "total=0 (foreign repo filter yields 0)"
else
  record GET "/api/schedules?limit=1&f=repo:any:${FOREIGN_REPO}" "$code" "LEAK" "total=${sched_ftotal} (foreign repo filter leaked count!)"
fi
if [[ -n "${FOREIGN_SCHED_ID:-}" ]]; then
  expect_denied GET "/api/schedules/${FOREIGN_SCHED_ID}/composition"
fi

expect_narrowed GET "/api/persistent-sandboxes" '"count=" + (length|tostring)'
expect_narrowed GET "/api/system/persistent-sandboxes" '"legacy alias count=" + (length|tostring)'
code=$(call_api "$GRAIN_API_TOKEN" GET "/api/persistent-sandboxes?limit=1")
ps_total=$(jq -r '.total // 0' "$TMP/body" 2>/dev/null || echo "err")
ps_items=$(jq -r '.items | length' "$TMP/body" 2>/dev/null || echo "err")
ps_cursor=$(jq -r '.nextCursor // ""' "$TMP/body" 2>/dev/null || echo "")
ps_decoded=""
if [[ -n "$ps_cursor" ]]; then
  ps_decoded=$(python3 -c 'import base64,sys; s=sys.argv[1]; s+="=" * (-len(s)%4); print(base64.urlsafe_b64decode(s).decode("utf-8","replace"))' "$ps_cursor" 2>/dev/null || true)
fi
if [[ "$code" == "200" && "$ps_decoded" != *"$FOREIGN_SANDBOX_NAME"* && "$ps_items" == "$ps_total" || ( "$code" == "200" && "$ps_decoded" != *"$FOREIGN_SANDBOX_NAME"* && "$ps_items" -ge 1 ) ]]; then
  record GET "/api/persistent-sandboxes?limit=1" "$code" "PASS" "total=${ps_total} items=${ps_items} cursor=${ps_decoded:-none}"
else
  record GET "/api/persistent-sandboxes?limit=1" "$code" "LEAK" "total=${ps_total} items=${ps_items} decoded_cursor=${ps_decoded}"
fi
code=$(call_api "$GRAIN_API_TOKEN" GET "/api/persistent-sandboxes?limit=1&q=${FOREIGN_SANDBOX_NAME}")
ps_qtotal=$(jq -r '.total // 0' "$TMP/body" 2>/dev/null || echo "err")
if [[ "$code" == "200" && "$ps_qtotal" == "0" ]]; then
  record GET "/api/persistent-sandboxes?limit=1&q=${FOREIGN_SANDBOX_NAME}" "$code" "PASS" "total=0 (no cross-repo oracle)"
else
  record GET "/api/persistent-sandboxes?limit=1&q=${FOREIGN_SANDBOX_NAME}" "$code" "LEAK" "total=${ps_qtotal} (cross-repo persistent-sandbox oracle!)"
fi
expect_denied GET "/api/persistent-sandboxes/${FOREIGN_SANDBOX_NAME}/logs"

# 6c. Templates & suites
code=$(call_api "$GRAIN_API_TOKEN" GET "/api/templates")
tmpl_count=$(jq -r 'length' "$TMP/body" 2>/dev/null || echo "err")
if [[ "$code" == "200" ]] && grep -qiF "$FOREIGN_SECRET_WORD" "$TMP/body"; then
  record GET "/api/templates" "$code" "LEAK" "count=${tmpl_count} leaked foreign template containing ${FOREIGN_SECRET_WORD}!"
elif [[ "$code" == "200" ]]; then
  record GET "/api/templates" "$code" "PASS" "count=${tmpl_count} (no foreign templates)"
else
  record GET "/api/templates" "$code" "FAIL" "expected 200"
fi
if [[ -n "${FOREIGN_TEMPLATE_ID:-}" ]]; then
  expect_denied GET "/api/templates/${FOREIGN_TEMPLATE_ID}/composition"
fi
expect_denied   GET "/api/suites"
expect_narrowed GET "/api/suite-runs" '"count=" + (length|tostring)'
if [[ -n "${FOREIGN_SUITE_RUN_ID:-}" ]]; then
  expect_denied GET "/api/suite-runs/${FOREIGN_SUITE_RUN_ID}"
fi

# 7. Repos, releases, and deployment-wide reads / writes
expect_narrowed GET "/api/deployment/reachable-repos" '"repos=" + ([.repos[]?.repo]|join(","))'
expect_narrowed GET "/api/repos/${OWN_REPO}/settings" '"own repo settings ok"'
expect_denied   GET "/api/repos/${FOREIGN_REPO}/settings"
expect_narrowed GET "/api/repos/${OWN_REPO}/releases" '"own repo releases ok"'
expect_denied   GET "/api/repos/${FOREIGN_REPO}/releases"
expect_narrowed GET "/api/repos/${OWN_REPO}/declarations" '"own repo declarations ok"'
expect_denied   GET "/api/repos/${FOREIGN_REPO}/declarations"
expect_narrowed GET "/api/deployment/repos/${OWN_REPO}/config" '"own repo config ok"'
expect_denied   GET "/api/deployment/repos/${FOREIGN_REPO}/config"

for path in \
  /api/release-approval-key \
  /api/git-ambient-sources \
  /api/pause \
  /api/pipeline-pause \
  /api/settings \
  /api/config \
  /api/secrets \
  /api/connections \
  /api/capabilities \
  /api/memory \
  /api/sandboxes \
  /api/metrics; do
  expect_denied GET "$path"
done

expect_denied POST "/api/tasks" '{"repo":"'"$OWN_REPO"'","title":"nope","description":"nope"}'
expect_denied PUT  "/api/settings" '{"maxWorkers":1}'

# 8. POST /api/notifications/release-pending
if [[ -n "${NORG_TOKEN:-}" ]]; then
  code=$(call_api "$NORG_TOKEN" POST "/api/notifications/release-pending" '{"version":"v9.9.1","body":"without release-gate-read"}')
  msg=$(jq -r '.error // empty' "$TMP/body" 2>/dev/null || head -c 120 "$TMP/body")
  if [[ "$code" == "403" ]]; then
    record POST "/api/notifications/release-pending (no release-gate-read)" "$code" "PASS" "${msg:0:90}"
  else
    record POST "/api/notifications/release-pending (no release-gate-read)" "$code" "LEAK" "expected 403, got $code"
  fi
fi
if [[ -n "${RELEASE_GATE_TOKEN:-}" ]]; then
  code=$(call_api "$RELEASE_GATE_TOKEN" POST "/api/notifications/release-pending" '{"version":"v9.9.1","body":"own repo pending release"}')
  nrepo=$(jq -r '.notification.repo // empty' "$TMP/body" 2>/dev/null || true)
  if [[ "$code" == "200" && "$nrepo" == "$OWN_REPO" ]]; then
    record POST "/api/notifications/release-pending (own v9.9.1)" "$code" "PASS" "raised with repo=${nrepo}"
  else
    record POST "/api/notifications/release-pending (own v9.9.1)" "$code" "FAIL" "got $code repo=${nrepo}: $(head -c 100 "$TMP/body")"
  fi
  expect_denied POST "/api/notifications/release-pending" '{"version":"v9.9.0","body":"try hijack foreign pending release"}' "$RELEASE_GATE_TOKEN"
fi

# 9. POST /api/tasks/from-template across every template grant class
probe_template() {
  local label=$1 body=$2 want_code=$3 want_state=${4:-}
  local code state tid msg
  code=$(call_api "$GRAIN_API_TOKEN" POST "/api/tasks/from-template" "$body")
  state=$(jq -r '.state // empty' "$TMP/body" 2>/dev/null || true)
  tid=$(jq -r '.id // empty' "$TMP/body" 2>/dev/null || true)
  msg=$(jq -r '.error // empty' "$TMP/body" 2>/dev/null || true)
  if [[ "$want_code" == "403" ]]; then
    if [[ "$code" == "403" ]]; then
      record POST "/api/tasks/from-template (${label})" "$code" "PASS" "${msg:0:90}"
    else
      record POST "/api/tasks/from-template (${label})" "$code" "LEAK" "expected 403, got $code state=${state} id=${tid}"
    fi
  else
    if [[ "$code" == "$want_code" && "$state" == "$want_state" ]]; then
      record POST "/api/tasks/from-template (${label})" "$code" "PASS" "id=${tid} state=${state}"
    else
      record POST "/api/tasks/from-template (${label})" "$code" "LEAK" "expected ${want_code}/${want_state}, got ${code}/${state} (${msg:0:80})"
    fi
  fi
}

probe_template "clean"              '{"template":"clean"}'                                  201 queued
probe_template "own-ci-read"        '{"template":"own-ci-read"}'                            201 queued
probe_template "own-grain-ci"       '{"template":"own-grain-ci"}'                           201 queued
probe_template "cross-repo-body"    '{"repo":"'"$FOREIGN_REPO"'","template":"clean"}'       403
probe_template "cross-read"         '{"template":"cross-read"}'                             403
probe_template "cross-write"        '{"template":"cross-write"}'                            403
probe_template "cross-api-grant"    '{"template":"cross-api-grant"}'                        403
probe_template "unscoped-api-grant" '{"template":"unscoped-api-grant"}'                     201 proposed
probe_template "non-ci-write-grant" '{"template":"non-ci-write-grant"}'                     201 proposed
probe_template "grain-daemon-grant" '{"template":"grain-daemon-grant"}'                     201 proposed
probe_template "dangerous-grant"    '{"template":"dangerous-grant"}'                        201 proposed
probe_template "open-network"       '{"template":"open-network"}'                           201 proposed
probe_template "admission-bypass"   '{"template":"admission-bypass"}'                       201 proposed
probe_template "high-priority"      '{"template":"high-priority"}'                          201 proposed
probe_template "keep-running"       '{"template":"keep-running"}'                           201 proposed
probe_template "declared-push"      '{"template":"declared-push"}'                          201 proposed

if [[ -n "${OUT_JSON:-}" ]]; then
  jq -s '.' "$RESULTS_FILE" >"$OUT_JSON"
fi

echo ""
echo "SUMMARY: failures_or_leaks=${FAILS}"
exit $(( FAILS > 0 ? 1 : 0 ))
