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

# Shared helpers for the docs/test-plans/2026-11-07.md §2 walkthrough.
# A scratch PostgreSQL cluster (Debian postgresql-15) runs as `debian` under
# /tmp/pgdata on 127.0.0.1:4290 with scram-sha-256 everywhere (pg-init.sh).
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
PGBIN=${PGBIN:-$(ls -d /usr/lib/postgresql/*/bin | sort -V | tail -1)}
PGPORT=${PGPORT:-4290}
PGPASS=${PGPASS:-pgpass-walk}
W=${W:-/tmp/walk}
mkdir -p "$W"

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
wait_gone() { for _ in $(seq 1 60); do kill -0 "$1" 2>/dev/null || return 0; sleep 1; done; return 1; }
tok() { cat "$1/local-api-token"; }
# api DATADIR PORT PATH [curl args...]
api() { local d=$1 p=$2 path=$3; shift 3; curl -s -H "Authorization: Bearer $(tok "$d")" -H 'Content-Type: application/json' "http://127.0.0.1:$p$path" "$@"; }
say() { printf '\n== %s\n' "$*"; }
