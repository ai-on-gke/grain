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

# run_all.sh runs every item on one store: DB=sqlite (default) or
# DB=postgres. Output goes to out/<db>-item<N>.log.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
DB=${DB:-sqlite}
export DB
OUT=${OUT:-$HERE/out}
mkdir -p "$OUT"
stop() { for f in /var/tmp/walk-sched-*/daemon.pid /var/tmp/walk-sched-*/sim.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null; done; sleep 2; }
stop
"$HERE/env.sh" >"$OUT/$DB-env.log" 2>&1 || { cat "$OUT/$DB-env.log"; exit 1; }
sleep 10
"$HERE/walk_item_1_hold.sh" >"$OUT/$DB-item1.log" 2>&1
"$HERE/walk_item_2_sandbox.sh" >"$OUT/$DB-item2.log" 2>&1
"$HERE/walk_item_3_own.sh" >"$OUT/$DB-item3.log" 2>&1
stop
"$HERE/walk_item_4_upgrade.sh" >"$OUT/$DB-item4.log" 2>&1
stop
echo "done: $OUT/$DB-item*.log"
