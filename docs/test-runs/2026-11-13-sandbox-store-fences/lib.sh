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

# Shared helpers for docs/test-plans/2026-11-13.md §12 item 2. A scratch
# PostgreSQL cluster (Debian postgresql-15) runs as `debian` under
# $W/pgdata on 127.0.0.1:4290 with scram-sha-256 (pg_up below).
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
PGBIN=${PGBIN:-$(ls -d /usr/lib/postgresql/*/bin | sort -V | tail -1)}
PGPORT=${PGPORT:-4290}
PGPASS=${PGPASS:-pgpass-walk}
W=${W:-/tmp/walk12}
mkdir -p "$W"

pg_up() {
  if [ ! -d "$W/pgdata" ]; then
    printf '%s\n' "$PGPASS" >"$W/pwfile"
    "$PGBIN/initdb" -D "$W/pgdata" -U grain --pwfile="$W/pwfile" -A scram-sha-256 >/dev/null
  fi
  "$PGBIN/pg_ctl" -D "$W/pgdata" -o "-p $PGPORT -k $W -c listen_addresses=127.0.0.1" -l "$W/pg.log" status >/dev/null 2>&1 ||
    "$PGBIN/pg_ctl" -D "$W/pgdata" -o "-p $PGPORT -k $W -c listen_addresses=127.0.0.1" -l "$W/pg.log" -w start >/dev/null
}
pgurl() { echo "postgres://grain:$PGPASS@127.0.0.1:$PGPORT/$1?sslmode=disable"; }
psql_g() { local db=$1; shift; PGPASSWORD=$PGPASS psql -h 127.0.0.1 -p "$PGPORT" -U grain -d "$db" -Atq "$@"; }
recreate_db() { psql_g postgres -c "DROP DATABASE IF EXISTS $1 WITH (FORCE)" -c "CREATE DATABASE $1"; }

# daemon NAME PORT DATADIR [extra flags...] -- starts `grain daemon` in the
# background and prints its pid. Log: $W/NAME.log.
daemon() {
  local name=$1 port=$2 data=$3; shift 3
  mkdir -p "$data" "$W/home-$name"
  HOME=$W/home-$name setsid "$GRAIN" daemon -data-dir "$data" -ui-addr "127.0.0.1:$port" \
    -sandbox-backend host -sandbox-dir "$W/sbx-$name" -sandbox-port-base=0 \
    -github-host 127.0.0.1:1 -github-insecure-http "$@" >"$W/$name.log" 2>&1 </dev/null &
  echo $!
}
wait_up() { for _ in $(seq 1 120); do curl -s -o /dev/null "http://127.0.0.1:$1/" && return 0; sleep 1; done; return 1; }
wait_gone() { for _ in $(seq 1 ${2:-60}); do kill -0 "$1" 2>/dev/null || return 0; sleep 1; done; return 1; }
tok() { cat "$1/local-api-token"; }
# api DATADIR PORT PATH [curl args...]
api() { local d=$1 p=$2 path=$3; shift 3; curl -s -H "Authorization: Bearer $(tok "$d")" -H 'Content-Type: application/json' "http://127.0.0.1:$p$path" "$@"; }
say() { printf '\n== %s\n' "$*"; }
# writer NAME DATADIR PORT TASK COUNT -- posts COUNT comments "NAME write i"
# to TASK, one every 0.2s, logging "epoch i http-code" per attempt.
writer() {
  local name=$1 d=$2 p=$3 t=$4 n=$5 i=0 c
  while [ $i -lt "$n" ]; do i=$((i+1))
    c=$(curl -s -o /dev/null -w '%{http_code}' -m 3 -H "Authorization: Bearer $(tok "$d")" -H 'Content-Type: application/json' \
      "http://127.0.0.1:$p/api/tasks/$t/comments" -d "{\"body\":\"$name write $i\"}")
    echo "$(date +%s.%N) $i $c"; sleep 0.2
  done
}
