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

# helpers.sh is sourced by the step scripts: `api`, `db`/`dbv` (SQLite or
# PostgreSQL, as env.sh started), `due`, `wait_sync` and `push_widgets`.
DB=${DB:-sqlite}
WORK_ROOT=${WORK_ROOT:-/var/tmp/walk-sched-tmpl-$DB}
UI_PORT=${UI_PORT:-4271}
API_URL=${API_URL:-http://127.0.0.1:${UI_PORT}}
PG_DB=${PG_DB:-walk_sched_tmpl}
SQLITE_DB="$WORK_ROOT/data/store/grain.db"
W="$WORK_ROOT/clones/acme_widgets"
TOK=$(cat "$WORK_ROOT/data/local-api-token")
export GRAIN_URL="$API_URL" GRAIN_TOKEN="$TOK"
api() { curl -sS -X "$1" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" ${3:+--data "$3"} "$API_URL$2"; }
# db prints a table; dbv prints bare values. Both take SQL written with
# double-quoted identifiers, which both stores accept.
db() {
  if [ "$DB" = postgres ]; then PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d "$PG_DB" -P pager=off -c "$1"
  else sqlite3 -header -column "$SQLITE_DB" "$1"; fi
}
dbv() {
  if [ "$DB" = postgres ]; then PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d "$PG_DB" -tAc "$1"
  else sqlite3 "$SQLITE_DB" "$1"; fi
}
# ts N renders now-N seconds the way each store keeps a schedule's times.
ts() {
  if [ "$DB" = postgres ]; then echo "now() - interval '${1:-1} seconds'"
  else echo "strftime('%Y-%m-%d %H:%M:%S +0000 UTC','now','-${1:-1} seconds')"; fi
}
# due makes the named schedules (stems) come due now.
due() {
  local ids=""
  for s in "$@"; do ids="$ids,'repo:schedule:acme:widgets:$s'"; done
  dbv "UPDATE schedule SET next_run_at = $(ts 1) WHERE id IN (${ids#,})" >/dev/null
}
wait_sync() { # wait_sync <sql> <want>
  for _ in $(seq 60); do [ "$(dbv "$1")" = "$2" ] && return 0; sleep 2; done
  echo "timed out waiting for: $1 = $2"; return 1
}
push_widgets() { # push_widgets <message>
  git -C "$W" add -A && git -C "$W" commit -q -m "$1" && git -C "$W" push -q origin main
}
# sched_tasks <stem> lists the tasks a schedule filed, newest first.
sched_tasks() {
  db "SELECT t.id, t.title, t.admission, t.sandbox_disk_gb AS disk, s.state FROM task t JOIN task_state s ON s.task_id=t.id
      WHERE t.id IN (SELECT task_id FROM task_tag WHERE tag='schedule:repo:schedule:acme:widgets:$1') ORDER BY t.created_at DESC LIMIT ${2:-5}"
}
step() { echo; echo "=== $*"; }
# close_open <stem>... closes every open task those schedules filed, so
# the next firing is not held as an overlap.
close_open() {
  for s in "$@"; do
    for id in $(dbv "SELECT t.task_id FROM task_state t JOIN task_tag g ON g.task_id=t.task_id WHERE t.state<>'closed' AND g.tag='schedule:repo:schedule:acme:widgets:$s'"); do
      api POST "/api/tasks/$id/close" '{"reason":"walkthrough"}' >/dev/null
    done
  done
}
# resync makes the next reconcile tick re-read acme/widgets' declarations
# (they are polled every 2 minutes) and waits until it has checked HEAD.
resync() {
  dbv "UPDATE repo_declaration_sync SET checked_at = $(ts 600) WHERE name='widgets'" >/dev/null
  for _ in $(seq 30); do
    [ "$(dbv "SELECT checked_at > $(ts 5) FROM repo_declaration_sync WHERE name='widgets'")" = "$( [ "$DB" = postgres ] && echo t || echo 1)" ] && break
    sleep 1
  done
  sleep 1
}
