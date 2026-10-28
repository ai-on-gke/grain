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

# post-reset-e2e.sh: the automatable rows of the post-reset end-to-end
# qualification (docs/test-plans/2026-10-20-post-reset-e2e.md), run
# against real daemons built from this tree.
#
# Each row starts a real `grain daemon` (no sandboxes: -sandbox-backend
# none) on a fresh data dir and drives it the way an operator would:
# the CLI, the HTTP API with the access code as a bearer, and the
# on-disk data dir. Nothing of grain's own is stubbed. The only stub is
# a fake `ggh` for the Google build's command-backed forge, put on the
# daemon's PATH.
#
# Usage:
#   scripts/post-reset-e2e.sh -build core|google -db sqlite|postgres \
#       [-grain BIN] [-old-grain BIN] [-out FILE]
#
#   -grain      the binary under test (default: built from this tree)
#   -old-grain  a v1.13.0 binary; when given, the upgrade/guard rows run
#               against a data dir it wrote
#   -out        where to write the result rows (markdown table rows)
#
# For -db postgres the script needs `initdb`/`pg_ctl` (Debian's
# postgresql package) and starts a throwaway cluster of its own.
#
# Exit status is 1 when any row FAILs; SKIP rows don't fail the run.
set -uo pipefail

BUILD=core
DB=sqlite
GRAIN=""
OLD_GRAIN=""
OUT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -build) BUILD=$2; shift 2 ;;
    -db) DB=$2; shift 2 ;;
    -grain) GRAIN=$2; shift 2 ;;
    -old-grain) OLD_GRAIN=$2; shift 2 ;;
    -out) OUT=$2; shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d /tmp/post-reset-e2e.XXXXXX)
OUT=${OUT:-$WORK/results.md}
: >"$OUT"
FAILS=0
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null; done
  if [[ -n "${PGDATA_DIR:-}" ]]; then "$PGBIN/pg_ctl" -D "$PGDATA_DIR" -m immediate stop >/dev/null 2>&1; fi
}
trap cleanup EXIT

if [[ -z "$GRAIN" ]]; then
  GRAIN=$WORK/grain
  if [[ $BUILD == google ]]; then pkg=./google/cmd/grain; else pkg=./cmd/grain; fi
  (cd "$ROOT" && go build -o "$GRAIN" "$pkg") || { echo "build failed" >&2; exit 1; }
fi

row() { # id flow result detail
  local id=$1 flow=$2 res=$3 detail=$4
  [[ $res == FAIL ]] && FAILS=$((FAILS + 1))
  printf '| %s | %s | %s/%s | **%s** | %s |\n' "$id" "$flow" "$BUILD" "$DB" "$res" "$detail" | tee -a "$OUT"
}

# --- Postgres -------------------------------------------------------------
PGURL_BASE=""
if [[ $DB == postgres ]]; then
  PGBIN=$(find /usr/lib/postgresql -mindepth 2 -maxdepth 2 -name bin -type d 2>/dev/null | sort -V | tail -1)
  [[ -x "$PGBIN/initdb" ]] || { echo "postgres requested but initdb not found" >&2; exit 2; }
  PGDATA_DIR=$WORK/pg
  PGPORT=$((15432 + RANDOM % 1000))
  "$PGBIN/initdb" -D "$PGDATA_DIR" -U grain -A trust >/dev/null
  "$PGBIN/pg_ctl" -D "$PGDATA_DIR" -o "-p $PGPORT -k $WORK -c listen_addresses=127.0.0.1" -l "$WORK/pg.log" start >/dev/null
  PGURL_BASE="postgres://grain@127.0.0.1:$PGPORT"
fi
new_db_flags() { # echoes the daemon flags for a fresh database
  if [[ $DB == postgres ]]; then
    # Called as $(new_db_flags), so the counter lives in a file: a shell
    # variable bumped in that subshell would hand every caller grain1.
    local n=$(($(cat "$WORK/dbn" 2>/dev/null || echo 0) + 1))
    echo "$n" >"$WORK/dbn"
    local DBN=$n
    "$PGBIN/createdb" -h 127.0.0.1 -p "$PGPORT" -U grain "grain$DBN"
    echo "-postgres-url $PGURL_BASE/grain$DBN?sslmode=disable"
  fi
}

# --- the fake ggh (Google build's command-backed forge) ---------------------
STUBS=$WORK/stubs
mkdir -p "$STUBS"
cat >"$STUBS/ggh" <<'EOF'
#!/bin/sh
echo "ggh $*" >>"${GGH_LOG:-/dev/null}"
case "$1 $2" in
  "auth token") echo "ggh-fake-token-not-a-secret"; exit 0 ;;
  "auth status") echo "Logged in as walker"; exit 0 ;;
esac
exit 1
EOF
chmod +x "$STUBS/ggh"
export GGH_LOG=$WORK/ggh.log

# --- daemons ----------------------------------------------------------------
PORT_BASE=$((18100 + RANDOM % 200))
SB_BASE=$(((40 + RANDOM % 20) * 1024))
start_daemon() { # dir extra-flags... ; sets URL TOKEN PID ; returns 1 if it exits
  local dir=$1; shift
  PORT_BASE=$((PORT_BASE + 1)); SB_BASE=$((SB_BASE + 1024))
  URL=http://127.0.0.1:$PORT_BASE
  PATH="$STUBS:$PATH" "$GRAIN" daemon -data-dir "$dir" -sandbox-backend none \
    -ui-addr "127.0.0.1:$PORT_BASE" -sandbox-port-base "$SB_BASE" "$@" >"$dir.log" 2>&1 &
  PID=$!
  PIDS+=("$PID")
  for _ in $(seq 1 60); do
    if ! kill -0 "$PID" 2>/dev/null; then return 1; fi
    if [[ -s $dir/local-api-token ]] && curl -s -o /dev/null -H "Authorization: Bearer $(cat "$dir/local-api-token")" "$URL/api/sandboxes"; then
      TOKEN=$(cat "$dir/local-api-token"); return 0
    fi
    sleep 0.5
  done
  return 1
}
stop_daemon() { kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; }
api() { # method path [body] -> prints "code body"
  local m=$1 p=$2 body=${3:-}
  if [[ -n $body ]]; then
    curl -s -o "$WORK/resp" -w '%{http_code}' -X "$m" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body" "$URL$p"
  else
    curl -s -o "$WORK/resp" -w '%{http_code}' -X "$m" -H "Authorization: Bearer $TOKEN" "$URL$p"
  fi
}
cli() { GRAIN_SERVER=$URL GRAIN_DATA_DIR=$DIR PATH="$STUBS:$PATH" "$GRAIN" "$@"; }
jq_py() { python3 -c "import json,sys; d=json.load(open('$WORK/resp')); $1"; }

# =========================================================================
# I1 fresh install boots; data dir layout; access code file mode
DIR=$WORK/d-fresh
FLAGS=$(new_db_flags)
# shellcheck disable=SC2086
if start_daemon "$DIR" $FLAGS; then
  mode=$(stat -c %a "$DIR/local-api-token")
  if [[ $mode == 600 && ! -e $DIR/tenancy ]]; then
    row I1 "Fresh install boots; access code 0600; no tenancy file" PASS "data dir: $(find "$DIR" -mindepth 1 -maxdepth 1 -printf '%f ' | tr ' ' '\n' | sort | tr '\n' ' ')"
  else
    row I1 "Fresh install boots; access code 0600; no tenancy file" FAIL "mode $mode; tenancy file present: $([[ -e $DIR/tenancy ]] && echo yes || echo no)"
  fi
else
  row I1 "Fresh install boots" FAIL "daemon exited: $(tail -3 "$DIR.log" | tr '\n' ' ')"
  echo "cannot continue without a daemon" >&2; exit 1
fi

# I2 access-code gate
c_none=$(curl -s -o /dev/null -w '%{http_code}' "$URL/")
c_api=$(curl -s -o /dev/null -w '%{http_code}' "$URL/api/tasks")
c_bad=$(curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH' "$URL/api/tasks")
c_good=$(api GET /api/tasks)
c_cli=$(cli access-code 2>/dev/null)
if [[ $c_none == 401 && $c_api == 401 && $c_bad == 401 && $c_good == 200 && $c_cli == "$TOKEN" ]]; then
  row I2 "Access-code sign-in (no code 401, wrong code 401, code 200; grain access-code prints it)" PASS "/: $c_none, /api/tasks: $c_api, wrong: $c_bad, right: $c_good"
else
  row I2 "Access-code sign-in" FAIL "/: $c_none, /api/tasks: $c_api, wrong: $c_bad, right: $c_good, cli matches: $([[ $c_cli == "$TOKEN" ]] && echo yes || echo no)"
fi
# login form: the code typed lower-case with spaces sets the cookie
code_lc=$(echo "$TOKEN" | tr '[:upper:]' '[:lower:]' | tr '-' ' ')
c_form=$(curl -s -o /dev/null -w '%{http_code}' -c "$WORK/jar" --data-urlencode "code=$code_lc" "$URL/api/auth/access-code")
c_cookie=$(curl -s -o /dev/null -w '%{http_code}' -b "$WORK/jar" "$URL/api/tasks")
if [[ $c_cookie == 200 ]]; then
  row I2b "Access-code login form (lower-case, spaces) sets a session cookie" PASS "POST /api/auth/access-code: $c_form; cookie → /api/tasks $c_cookie"
else
  row I2b "Access-code login form sets a session cookie" FAIL "POST /api/auth/access-code: $c_form; cookie → /api/tasks $c_cookie"
fi

# U1 no team, account, admin or hub routes
bad=""
for p in /api/teams /api/teams/default /api/users /api/auth/login /api/auth/register /api/auth/change-password /api/hub/status; do
  c=$(api GET "$p"); [[ $c == 404 ]] || bad+="$p=$c "
done
c=$(api POST /api/tasks/1/move '{}'); [[ $c == 404 || $c == 405 ]] || bad+="move=$c "
if [[ -z $bad ]]; then
  row U1 "No team/account/admin/hub API routes" PASS "all 404"
else
  row U1 "No team/account/admin/hub API routes" FAIL "$bad"
fi

# C3 git ambient sources: ggh in Google, none in core
api GET /api/git-ambient-sources >/dev/null
srcs=$(jq_py 'print(",".join(sorted(str(s.get("name", s) if isinstance(s, dict) else s) for s in d)))')
if [[ $BUILD == google && $srcs == *ggh* ]] || [[ $BUILD == core && $srcs != *ggh* ]]; then
  row C3 "Command-backed forge advertised only in the Google build" PASS "sources: [${srcs}]"
else
  row C3 "Command-backed forge advertised only in the Google build" FAIL "sources: [${srcs}]"
fi
if [[ $BUILD == core ]]; then
  out=$(cli git-ambient -source ggh -host git.corp.example 2>&1); rc=$?
  if [[ $rc -ne 0 && $out != *panic* ]]; then
    row C3b "Core refuses the ggh source cleanly" PASS "exit $rc: $(echo "$out" | head -1 | cut -c1-160)"
  else
    row C3b "Core refuses the ggh source cleanly" FAIL "exit $rc: $(echo "$out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
fi

# C5 default model on a fresh install: none, until one is set
api GET /api/deployment >/dev/null
dm=$(jq_py 'print(d.get("defaultModel",""))')
if [[ -z $dm ]]; then
  row C5 "Fresh install has no default model" PASS "defaultModel unset"
else
  row C5 "Fresh install has no default model" FAIL "defaultModel=$dm"
fi

# I4 grain connections import, twice; token file 0600
echo "ghp_qualificationFakeToken0000000000000000" >"$WORK/bot.token"
SECRET_GEMINI=${GEMINI_API_KEY:-fake-gemini-key-for-qualification}
printf '%s' "$SECRET_GEMINI" >"$WORK/gemini.key"
cat >"$WORK/seed.yaml" <<EOF
git:
  - credential: bot
    host: github.com
    token_file: $WORK/bot.token
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro-preview]
    credential_file: $WORK/gemini.key
default_model: gemini-3.1-pro-preview
EOF
o1=$(cli connections -data-dir "$DIR" import "$WORK/seed.yaml" 2>&1); r1=$?
o2=$(cli connections -data-dir "$DIR" import "$WORK/seed.yaml" 2>&1); r2=$?
tmode=$(stat -c %a "$DIR/secrets/github/bot.token" 2>/dev/null)
gc=$(cli git-connections 2>&1)
api GET /api/deployment >/dev/null
dm=$(jq_py 'print(d.get("defaultModel",""))')
if [[ $r1 -eq 0 && $r2 -eq 0 && $tmode == 600 && $gc == *bot* && $dm == gemini-3.1-pro-preview ]]; then
  row I4 "grain connections import (git + model + default model), idempotent" PASS "$(echo "$o2" | tail -1 | cut -c1-120); token 0600; default model read back"
else
  row I4 "grain connections import" FAIL "rc $r1/$r2; token mode $tmode; default=$dm; $(echo "$o1 $o2" | tail -2 | tr '\n' ' ' | cut -c1-200)"
fi

# L1 create from the CLI, approve, close, reopen, close
out=$(cli create -title "qualification: CLI task" -repo acme/widgets -body "filed by post-reset-e2e" 2>&1)
tid=$(echo "$out" | grep -oE '[0-9]+' | head -1)
ok=1; steps="create: ${out:0:80}"
if [[ -n $tid ]]; then
  cli approve "$tid" >/dev/null 2>&1 || ok=0
  api GET "/api/tasks/$tid" >/dev/null; st1=$(jq_py 'print(d.get("state",""))')
  cli close "$tid" >/dev/null 2>&1 || ok=0
  # A close that meets a dispatch attempt in flight lands on the next
  # reconcile tick rather than at once, so wait for it.
  for _ in $(seq 1 30); do
    api GET "/api/tasks/$tid" >/dev/null; st2=$(jq_py 'print(d.get("state",""))')
    [[ $st2 == closed ]] && break; sleep 1
  done
  cli reopen "$tid" >/dev/null 2>&1 || ok=0
  api GET "/api/tasks/$tid" >/dev/null; st3=$(jq_py 'print(d.get("state",""))')
  steps="task $tid: approved→$st1, closed→$st2, reopened→$st3"
  [[ $st2 == closed && $st3 != closed ]] || ok=0
else ok=0; fi
row L1 "Task from the CLI: create, approve, close, reopen" "$([[ $ok == 1 ]] && echo PASS || echo FAIL)" "$steps"

# L2 create from the API (what the UI's New task posts), with depends_on and not_before
c=$(api POST /api/tasks "{\"title\":\"qualification: API task\",\"repo\":\"acme/widgets\",\"description\":\"x\",\"dependsOn\":[\"$tid\"],\"deferredUntil\":\"2099-01-01T00:00:00Z\"}")
nid=$(jq_py 'print(d.get("id",""))' 2>/dev/null)
api GET "/api/tasks/$nid" >/dev/null
deps=$(jq_py 'print(d.get("dependsOn") or d.get("depends_on") or "")' 2>/dev/null)
nb=$(jq_py 'print(d.get("deferredUntil") or "")' 2>/dev/null)
if [[ $c == 20? && -n $nid && $deps == *$tid* && $nb == 2099* ]]; then
  row L2 "Task from the API with depends_on and not_before" PASS "task $nid: dependsOn $deps, deferredUntil $nb"
else
  row L2 "Task from the API with depends_on and not_before" FAIL "POST $c id=$nid dependsOn=$deps deferredUntil=$nb: $(head -c 200 "$WORK/resp")"
fi

# S1 standing work: a cron schedule, a template, a suite, a board
c1=$(api POST /api/templates '{"name":"qualification template","title":"qualification from template","description":"t","repo":"acme/widgets"}')
tpl=$(jq_py 'print(d.get("id",""))' 2>/dev/null)
c2=$(api POST /api/schedules "{\"templateId\":\"$tpl\",\"repo\":\"acme/widgets\",\"base\":\"main\",\"recurrence\":{\"kind\":\"daily\",\"timeOfDay\":\"03:17\"},\"enabled\":false}")
r2=$(head -c 160 "$WORK/resp")
sch=$(jq_py 'print(d.get("id",""))' 2>/dev/null)
c3=$(api POST /api/suites "{\"name\":\"qualification suite\",\"templateIds\":[\"$tpl\"],\"mode\":\"count\",\"count\":1}")
r3=$(head -c 160 "$WORK/resp")
c4=$(api POST /api/boards '{"name":"qualification board"}')
api GET /api/schedules >/dev/null; nsch=$(jq_py 'print(len(d) if isinstance(d,list) else len(d.get("schedules",[])))' 2>/dev/null)
if [[ $c1 == 20? && $c2 == 20? && $c3 == 20? && $c4 == 20? ]]; then
  row S1 "Standing work: template, cron schedule, suite, board created and listed" PASS "templates $c1 ($tpl), schedules $c2 ($sch; $nsch listed), suites $c3, boards $c4"
else
  row S1 "Standing work: template, cron schedule, suite, board" FAIL "templates $c1, schedules $c2 ($r2), suites $c3 ($r3), boards $c4"
fi
if [[ -n $sch ]]; then
  c=$(api POST "/api/schedules/$sch/run")
  row S2 "Schedule run-now files a task" "$([[ $c == 20? ]] && echo PASS || echo FAIL)" "POST /api/schedules/$sch/run: $c $(head -c 120 "$WORK/resp")"
fi

# L3 pipeline pause and resume, model pause lift
o1=$(cli pause -set -reason qualification 2>&1); r1=$?
api GET /api/pipeline-pause >/dev/null; p1=$(head -c 200 "$WORK/resp")
o2=$(cli pause -resume 2>&1); r2=$?
c3=$(api DELETE "/api/pause?connection=gemini&model=gemini-3.1-pro-preview")
if [[ $r1 -eq 0 && $r2 -eq 0 && $c3 == 20? ]]; then
  row L3 "Pipeline pause/resume; per-model pause lift" PASS "set/resume exit 0; DELETE /api/pause?connection&model: $c3"
else
  row L3 "Pipeline pause/resume; per-model pause lift" FAIL "set $r1 ($o1), resume $r2 ($o2), lift $c3; $p1"
fi

# X1 secrets never logged
leak=""
for s in "$(cat "$WORK/bot.token")" "$SECRET_GEMINI" "$TOKEN"; do
  if grep -rqF -- "$s" "$DIR.log" "$DIR"/*.log 2>/dev/null; then leak+="${s:0:6}… "; fi
done
row X1 "Secrets (git token, model key, access code) never in the daemon log" "$([[ -z $leak ]] && echo PASS || echo FAIL)" "${leak:-none found}"

# I3 restart on its own dir is clean (guard accepts what it wrote)
stop_daemon
# shellcheck disable=SC2086
if start_daemon "$DIR" $FLAGS; then
  api GET "/api/tasks/$tid" >/dev/null
  row I3 "Restart on a dir this build wrote (guard accepts it)" PASS "task $tid still there"
else
  row I3 "Restart on a dir this build wrote" FAIL "$(tail -3 "$DIR.log" | tr '\n' ' ' | cut -c1-200)"
fi

# R1 export and import round trip
stop_daemon
if cli export -data-dir "$DIR" -out "$WORK/bundle.tar.gz" >"$WORK/export.log" 2>&1; then
  # A SQLite bundle carries the database. A Postgres bundle carries only
  # db-config.json and leaves the rows in the database it names (the
  # source daemon is stopped), so the imported dir runs on that.
  DIR2=$WORK/d-imported
  FLAGS2=""
  if "$GRAIN" import -data-dir "$DIR2" -in "$WORK/bundle.tar.gz" >"$WORK/import.log" 2>&1; then
    # shellcheck disable=SC2086
    if start_daemon "$DIR2" $FLAGS2; then
      api GET "/api/tasks/$tid" >/dev/null; t=$(jq_py 'print(d.get("title",""))')
      api GET /api/deployment >/dev/null; dm=$(jq_py 'print(d.get("defaultModel",""))')
      api GET /api/templates >/dev/null; nt=$(jq_py 'print(len(d) if isinstance(d,list) else len(d.get("templates",[])))')
      if [[ $t == qualification* && $dm == gemini-3.1-pro-preview && $nt -ge 1 ]]; then
        row R1 "Export/import round trip (tasks, default model, templates)" PASS "task $tid, default model and $nt template(s) survive"
      else
        row R1 "Export/import round trip" FAIL "task title='$t', default model='$dm', templates=$nt"
      fi
      stop_daemon
    else
      row R1 "Export/import round trip" FAIL "imported dir won't start: $(tail -3 "$DIR2.log" | tr '\n' ' ' | cut -c1-200)"
    fi
  else
    row R1 "Export/import round trip" FAIL "import: $(tail -3 "$WORK/import.log" | tr '\n' ' ' | cut -c1-200)"
  fi
else
  row R1 "Export/import round trip" FAIL "export: $(tail -3 "$WORK/export.log" | tr '\n' ' ' | cut -c1-200)"
fi

# I5 guard: a data dir written by v1.13.0 is refused, untouched
if [[ -n $OLD_GRAIN ]]; then
  OLD=$WORK/d-v113
  OFLAGS=$(new_db_flags)
  mkdir -p "$OLD"
  # shellcheck disable=SC2086
  "$OLD_GRAIN" daemon -data-dir "$OLD" -sandbox-backend none -ui-addr 127.0.0.1:$((PORT_BASE + 50)) \
    -sandbox-port-base $((SB_BASE + 8192)) $OFLAGS >"$OLD.log" 2>&1 &
  op=$!
  for _ in $(seq 1 60); do [[ -s $OLD/local-api-token ]] && break; sleep 0.5; done
  sleep 3; kill "$op" 2>/dev/null; wait "$op" 2>/dev/null
  before=$(cd "$OLD" && find . -type f -exec sha256sum {} + | sort | sha256sum)
  # shellcheck disable=SC2086
  if start_daemon "$OLD" $OFLAGS; then
    row I5 "Guard refuses a v1.13.0 (pre-reset) data dir" FAIL "the new build started on it"
    stop_daemon
  else
    after=$(cd "$OLD" && find . -type f -exec sha256sum {} + | sort | sha256sum)
    msg=$(grep -m1 -iE 'single-tenant|schema' "$OLD.log" | cut -c1-220)
    if [[ $msg == *single-tenant* && $before == "$after" ]]; then
      row I5 "Guard refuses a v1.13.0 (pre-reset) data dir, untouched, with a fresh-dir instruction" PASS "$msg"
    else
      row I5 "Guard refuses a v1.13.0 data dir, untouched" FAIL "dir unchanged: $([[ $before == "$after" ]] && echo yes || echo no); $(tail -2 "$OLD.log" | tr '\n' ' ' | cut -c1-200)"
    fi
  fi
else
  row I5 "Guard refuses a v1.13.0 (pre-reset) data dir" SKIP "no -old-grain given"
fi

echo
echo "results: $OUT; $FAILS failing row(s)"
[[ $FAILS -eq 0 ]]
