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

# Runs §3 items 1-6 in order, writing each transcript beside this script
# (stepN.out.txt). Needs PostgreSQL on 127.0.0.1:5432 with a superuser role
# grain/grain, $GRAIN built from HEAD and $GRAIN_OLD from v3.8.0.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
status=0
for s in step1-v380-to-head step2-sqlite-to-postgres step3-staterepo-restore-and-merge \
	step4-startup-self-heal step5-two-daemons step6-custom-schema; do
	echo "== $s"
	if bash "$HERE/$s.sh" >"$HERE/${s%%-*}.out.txt" 2>&1; then echo "   ok"; else echo "   FAILED"; status=1; fi
done
exit $status
