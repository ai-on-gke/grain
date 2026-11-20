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

# walk.sh drives docs/test-plans/2026-11-13.md §10 items 1-3 against an
# image-mode grain daemon that images.sh's registry feeds, started as
#
#   grain daemon -data-dir $D/data -sandbox-port-base=0 -disable-native-sandboxes \
#     -ui-addr 127.0.0.1:4281 -upgrade-image localhost:5000/acme/grain \
#     -upgrade-image-ref-file $D/image.env -upgrade-allowed-branches '**' \
#     -upgrade-grace-window 30s -upgrade-restart-cmd=$D/restart.sh [-postgres-url …]
#
# with $D/image.env reading GRAIN_IMAGE=localhost:5000/acme/grain:v1 and
# restart.sh only appending the time to $D/restarted (so "cut over" is
# observable without anything restarting).
#
# SQL is how a run in flight is stood in for: one task_run row with no
# finished_at is exactly what the scheduled upgrade's BusyRunCount waits
# on. Set SQL to a command that runs one statement given as its last
# argument, e.g.
#
#   SQL="sqlite3 $D/data/store/grain.db"
#   SQL="psql -qAt postgres://walk:walk@127.0.0.1:5432/grainwalk -c"
#
# Each step prints what it saw; walk.sh does not judge, the report does.
set -uo pipefail
D=${D:?set D to the daemon directory}
G=${G:-/home/debian/bin/grain}
REG=${REG:-localhost:5000/acme/grain}
SQL=${SQL:?set SQL, see above}
export GRAIN_SERVER=${GRAIN_SERVER:-http://127.0.0.1:4281} GRAIN_DATA_DIR=$D/data

settle() {
	for _ in $(seq 60); do
		sleep 2
		$G upgrade status 2>&1 | grep -q 'phase=running' || return 0
	done
}
show() {
	echo "## $1"
	$G upgrade status 2>&1 | sed 's/^/   /'
	echo "   ref file: $(cat "$D/image.env")"
	echo "   images:   $(docker images --format '{{.Repository}}:{{.Tag}}' | grep -v registry | sort | tr '\n' ' ')"
	echo "   containers: $(docker ps -a --filter label=grain.upgrade-check --format '{{.ID}} {{.Status}}' | tr '\n' ' ')"
}

echo "# item 1a: cancel inside the grace window"
$G upgrade start -image "$REG:v2"
sleep 3
$G upgrade cancel -reason "walk: inside grace window"
sleep 2
show "after the grace-window cancel"

echo "# item 1b: cancel during staging (the :slow health check)"
$G upgrade start -image "$REG:slow"
for _ in $(seq 60); do
	sleep 2
	$G upgrade status 2>&1 | grep -q 'health check' && break
done
show "health check running"
$G upgrade cancel -reason "walk: during staging"
sleep 3
show "after the staging cancel"

echo "# item 3: -after-tasks-finish behind a run in flight"
$SQL "insert into task_run(id,task_id,sandbox,attempt,started_at) values('walk-run-1','walk-task-1','walk-sbx',1,CURRENT_TIMESTAMP)"
$G upgrade start -after-tasks-finish v2
sleep 12
show "scheduled, run in flight"
$G upgrade cancel -scheduled -reason "walk: cancel while waiting"
sleep 12
show "after cancel -scheduled"
$G upgrade start -after-tasks-finish v2
sleep 12
$SQL "update task_run set finished_at=CURRENT_TIMESTAMP, outcome='success' where id='walk-run-1'"
echo "   run finished at $(date -u +%T)"
for _ in $(seq 40); do
	sleep 3
	$G upgrade status 2>&1 | grep -q 'branch=v2 phase=\(running\|ok\)' && break
done
echo "   upgrade seen at $(date -u +%T)"
settle
show "the scheduled upgrade, once the run finished"

echo "# item 2: cancel past the point of no return"
$G upgrade cancel -reason "walk: too late"
echo "   cancel exit=$?"
echo "   restarted: $(cat "$D/restarted" 2>/dev/null)"
