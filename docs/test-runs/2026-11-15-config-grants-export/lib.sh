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

# docs/test-plans/2026-11-15.md §6: grain/config grants-only repo access
# export across versions. Reuses the 2026-11-13 real-daemon harness
# (env.sh: start_daemon, api, store_exec) and adds seeding and dumping of
# the template/schedule read and write tables.

HERE15=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$HERE15/../2026-11-13-grain-health-exit-codes/env.sh"

# seed_access creates two templates and one schedule holding every
# repository-access shape §6 names: plain reads/writes on the default
# host, an off-host ghe.example.com read and a write with no base.
seed_access() {
	api POST /api/templates '{"name":"walk-plain","title":"Walk plain access","description":"Touch walk.txt in every write target.","repo":"acme/widgets","base":"main","reads":["acme/docs"],"writes":[{"repo":"acme/gadgets","base":"main"}]}'
	echo
	api POST /api/templates '{"name":"walk-edge","title":"Walk edge access","description":"Off-host read and a NULL-base write.","repo":"acme/widgets","base":"main","reads":["acme/docs","ghe.example.com/acme/legacy"],"writes":[{"repo":"acme/gadgets"},{"repo":"acme/docs","base":"release"}]}'
	echo
	api POST /api/templates '{"name":"walk-nobase","title":"Walk base-less write","description":"A write with no base, dispatchable without an off-host connection.","repo":"acme/widgets","base":"main","reads":["acme/docs"],"writes":[{"repo":"acme/gadgets"}]}'
	echo
	api POST /api/schedules '{"title":"Walk schedule","description":"Nightly walk.","repo":"acme/widgets","base":"main","recurrence":{"kind":"daily","timeOfDay":"03:00"},"reads":["acme/docs","ghe.example.com/acme/legacy"],"writes":[{"repo":"acme/gadgets","base":"main"},{"repo":"acme/docs"}]}'
	echo
}

# dump_access prints the four tables in a stable order.
dump_access() {
	local t
	for t in template_read template_write schedule_read schedule_write; do
		echo "-- $t"
		if [[ ${STORE:-sqlite} == sqlite ]]; then
			sqlite3 -cmd ".timeout 5000" -header "${1:-$WORK_DIR/data/store/grain.db}" "SELECT * FROM $t ORDER BY 1,2,3" 2>&1
		else
			PGPASSWORD=grain psql -q -h 127.0.0.1 -U grain -d grain_walk -A -c "SELECT * FROM $t ORDER BY 1,2,3"
		fi
	done
}
