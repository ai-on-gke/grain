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

# Drive one task to `failed` (model.MaxConsecutiveFailures crashed runs)
# for item3_stalled.sh; its id lands in $WORK_DIR/failed-blocker.id.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
set +e
F=$(new_task "blocker failed (5 crashes)" '{"kind":"usage_limit","message":"boom: the agent crashed"}')
echo "$F" >"$WORK_DIR/failed-blocker.id"
for _ in $(seq 60); do
	s=$(state "$F")
	echo "$(date +%T) $s"
	[[ $s == failed ]] && exit 0
	api POST "/api/tasks/$F/run-now" '{}' >/dev/null
	sleep 15
done
exit 1
