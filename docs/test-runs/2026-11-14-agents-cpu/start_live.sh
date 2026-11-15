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

# Leaves a real daemon up with two loaded host runs (1 and 2 cores, for
# ${SECS:-900} s) for the browser walk in keyboard_probe.mjs.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
SECS=${SECS:-900}
init_env /tmp/walk-cpu
start_ghsim
MAX_WORKERS=2 start_daemon "$GRAIN" "$WORK_DIR"
for spec in "Load one core:1" "Load two cores:2"; do
	id=$(api POST /api/tasks "$(jq -nc --arg t "${spec%%:*}" '{title:$t, description:"agents-cpu walkthrough load", repo:"acme/widgets"}')" | jq -r '.id // .task.id')
	jq -nc --arg c "stress-ng --cpu ${spec##*:} --cpu-method fft --timeout ${SECS}s --quiet" '{kind:"mcp_sequence", calls:[{tool:"run_command", args:{command:$c, description:"cpu load", timeout:1800000}}, {tool:"nothing_needed", args:{reason:"load finished"}}]}' >"$WORK_DIR/action-$id.json"
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "task $id: ${spec%%:*}"
done
echo "url $DAEMON code $CODE"
