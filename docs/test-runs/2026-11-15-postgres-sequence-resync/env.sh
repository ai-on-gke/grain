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

# Shared helpers for docs/test-plans/2026-11-15.md §3 (PostgreSQL id sequence
# resync). Source it, then call the functions: a real `grain daemon` on
# PostgreSQL at 127.0.0.1:${DAEMON_PORT}, writes through its HTTP API, and a
# per-table report of each serial sequence's last_value against MAX(id).

set -euo pipefail

GRAIN=${GRAIN:-/home/debian/bin/grain}
GRAIN_OLD=${GRAIN_OLD:-/home/debian/bin/grain-v380}
DAEMON_PORT=${DAEMON_PORT:-4290}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
PG_DB=${PG_DB:-grain_seq}
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/${PG_DB}?sslmode=disable}
WORK_DIR=${WORK_DIR:-/home/debian/walk/seq}

psql_db() { PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d "$PG_DB" -v ON_ERROR_STOP=1 -qAt "$@"; }

reset_db() {
	sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS ${PG_DB} WITH (FORCE);"
	sudo -u postgres psql -q -c "CREATE DATABASE ${PG_DB} OWNER grain;"
}

reset_work() {
	stop_daemon
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/home"
}

# seq_report [schema] prints "table.column last_value is_called max status"
# for every serial column in schema (default: the DSN's current schema).
# status is BEHIND when the next nextval() would collide with MAX(id).
seq_report() {
	local schema=${1:-public}
	psql_db -F ' ' <<SQL
DO \$\$ BEGIN PERFORM 1; END \$\$;
CREATE TEMP TABLE _r(t text, c text, lv bigint, ic bool, mx bigint);
DO \$\$
DECLARE r record; lv bigint; ic bool; mx bigint;
BEGIN
  FOR r IN SELECT tc.relname AS t, a.attname AS c,
                  pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) AS s,
                  tn.nspname AS n
           FROM pg_class tc JOIN pg_namespace tn ON tn.oid = tc.relnamespace
           JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum > 0 AND NOT a.attisdropped
           WHERE tn.nspname = '${schema}' AND tc.relkind = 'r'
             AND pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) IS NOT NULL
  LOOP
    EXECUTE format('SELECT last_value, is_called FROM %s', r.s) INTO lv, ic;
    EXECUTE format('SELECT MAX(%I) FROM %I.%I', r.c, r.n, r.t) INTO mx;
    INSERT INTO _r VALUES (r.t, r.c, lv, ic, mx);
  END LOOP;
END \$\$;
SELECT t || '.' || c, lv, ic, COALESCE(mx::text, '-'),
       CASE WHEN mx IS NULL THEN 'empty'
            WHEN (ic AND lv < mx) OR (NOT ic AND lv <= mx) THEN 'BEHIND'
            ELSE 'ok' END
FROM _r ORDER BY 1;
SQL
}

# seq_summary [schema]: only the populated or behind rows, as a compact table.
seq_summary() { seq_report "${1:-public}" | awk '$4 != "-" || $5 == "BEHIND"' | awk '{printf "%-44s %8s %-5s %8s %s\n", $1,$2,$3,$4,$5}'; }

stop_daemon() {
	local pidf="$WORK_DIR/daemon${1:-}.pid"
	if [[ -f "$pidf" ]]; then
		local pid
		pid=$(cat "$pidf")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
		rm -f "$pidf"
	fi
}

# start_daemon BIN [extra flags...]: a real daemon on PostgreSQL, no repo,
# no dispatch, waiting until the API answers with the local API token.
start_daemon() {
	local bin=$1
	shift
	local suffix=${DAEMON_SUFFIX:-}
	local data=${DATA_DIR:-$WORK_DIR/data}
	local log="$WORK_DIR/daemon${suffix}.log"
	touch "$log"
	env -i PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" HOME="$WORK_DIR/home" \
		GCE_METADATA_HOST="127.0.0.1:9" ${DAEMON_ENV:-} \
		setsid "$bin" daemon \
		-data-dir "$data" \
		-sandbox-dir "$WORK_DIR/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-max-workers 1 \
		-max-mergers 0 \
		$( [[ ${STORE:-postgres} == postgres ]] && echo -postgres-url "$PG_DSN" ) \
		"$@" >>"$log" 2>&1 </dev/null &
	echo $! >"$WORK_DIR/daemon${suffix}.pid"
	for _ in $(seq 600); do
		if [[ -s "$data/local-api-token" ]]; then
			TOKEN=$(tr -d '[:space:]' <"$data/local-api-token")
			if curl -sf -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:${DAEMON_PORT}/api/config" >/dev/null 2>&1; then
				export TOKEN
				return 0
			fi
		fi
		kill -0 "$(cat "$WORK_DIR/daemon${suffix}.pid")" 2>/dev/null || { echo "daemon exited; see $log" >&2; tail -20 "$log" >&2; return 1; }
		sleep 0.2
	done
	echo "daemon did not answer; see $log" >&2
	return 1
}

# api METHOD PATH [JSON]: prints "HTTP_CODE BODY".
api() {
	local m=$1 p=$2 body=${3:-}
	local args=(-s -o "$WORK_DIR/.body" -w '%{http_code}' -X "$m" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')
	[[ -n $body ]] && args+=(--data "$body")
	local code
	code=$(curl "${args[@]}" "$DAEMON$p")
	echo "$code $(head -c 400 "$WORK_DIR/.body")"
}

# writes LABEL: file a task, comment on it, save a setting, create a template
# and a suite holding it (a suite_item row). Prints each call's HTTP code and
# fails on anything that is not 2xx or that mentions SQLSTATE 23505.
writes() {
	local label=$1 out code id tid
	out=$(api POST /api/tasks "{\"title\":\"$label task\",\"description\":\"sequence walk\",\"noRepo\":true}")
	code=${out%% *}
	id=$(sed -E 's/.*"id":"?([0-9]+)"?.*/\1/' <<<"${out#* }" | head -c 20)
	echo "$label POST /api/tasks -> $code id=$id"
	check_ok "$out"
	out=$(api POST "/api/tasks/$id/comments" "{\"body\":\"$label comment\"}")
	echo "$label POST /api/tasks/$id/comments -> ${out%% *} $(grep -oE '"id":[0-9]+' <<<"$out" | head -1)"
	check_ok "$out"
	out=$(api POST /api/settings "{\"pollInterval\":\"$((RANDOM % 20 + 5))s\"}")
	echo "$label POST /api/settings -> ${out%% *}"
	check_ok "$out"
	out=$(api POST /api/templates "{\"name\":\"$label-$RANDOM\",\"title\":\"$label template\",\"description\":\"walk\"}")
	tid=$(grep -oE '"id":"[^"]+"' <<<"$out" | head -1 | cut -d'"' -f4)
	echo "$label POST /api/templates -> ${out%% *} id=$tid"
	check_ok "$out"
	out=$(api POST /api/suites "{\"name\":\"$label suite $RANDOM\",\"templateIds\":[\"$tid\"],\"mode\":\"count\",\"count\":1}")
	echo "$label POST /api/suites -> ${out%% *} $(grep -oE '"id":"[^"]+"' <<<"$out" | head -1)"
	check_ok "$out"
}

check_ok() {
	local code=${1%% *}
	if [[ $1 == *"SQLSTATE 23505"* || $1 == *duplicate\ key* || $code != 2* ]]; then
		echo "FAILED: $1" >&2
		return 1
	fi
}

# nextval_probe [schema]: draws one value from every serial sequence in schema
# and checks it lands above MAX(col) -- the next row any of those tables would
# get. Prints one summary line and every collision.
nextval_probe() {
	local schema=${1:-public}
	psql_db -F ' ' <<SQL
CREATE TEMP TABLE _p(t text, nv bigint, mx bigint);
DO \$\$
DECLARE r record; nv bigint; mx bigint;
BEGIN
  FOR r IN SELECT tc.relname AS t, a.attname AS c, tn.nspname AS n,
                  pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) AS s
           FROM pg_class tc JOIN pg_namespace tn ON tn.oid = tc.relnamespace
           JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum > 0 AND NOT a.attisdropped
           WHERE tn.nspname = '${schema}' AND tc.relkind = 'r'
             AND pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) IS NOT NULL
  LOOP
    EXECUTE format('SELECT nextval(%L)', r.s) INTO nv;
    EXECUTE format('SELECT MAX(%I) FROM %I.%I', r.c, r.n, r.t) INTO mx;
    INSERT INTO _p VALUES (r.t, nv, mx);
  END LOOP;
END \$\$;
SELECT 'nextval probe: ' || count(*) || ' serial tables, ' || count(*) FILTER (WHERE nv <= COALESCE(mx, 0)) || ' would collide' FROM _p;
SELECT 'COLLIDES ' || t || ' nextval=' || nv || ' max=' || mx FROM _p WHERE nv <= COALESCE(mx, 0) ORDER BY t;
SQL
}

# break_sequences [schema]: rewinds every populated table's serial sequence
# in schema to 1 (is_called=false), the state a pre-resync rebuild left.
break_sequences() {
	local schema=${1:-public}
	psql_db <<SQL
DO \$\$
DECLARE r record; mx bigint;
BEGIN
  FOR r IN SELECT tc.relname AS t, a.attname AS c, tn.nspname AS n,
                  pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) AS s
           FROM pg_class tc JOIN pg_namespace tn ON tn.oid = tc.relnamespace
           JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum > 0 AND NOT a.attisdropped
           WHERE tn.nspname = '${schema}' AND tc.relkind = 'r'
             AND pg_get_serial_sequence(format('%I.%I', tn.nspname, tc.relname), a.attname) IS NOT NULL
  LOOP
    EXECUTE format('SELECT MAX(%I) FROM %I.%I', r.c, r.n, r.t) INTO mx;
    IF mx IS NOT NULL THEN PERFORM setval(r.s, 1, false); END IF;
  END LOOP;
END \$\$;
SQL
}
