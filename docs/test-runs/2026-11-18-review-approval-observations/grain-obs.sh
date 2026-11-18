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

# Wraps the grain binary so the 2026-11-14 review-context harness (whose
# start_daemon runs the daemon under `env -i`) starts it with
# GRAIN_GATE_OBSERVATION_TASKS read from $OBS_ALLOWLIST_FILE (default
# /home/debian/obs-allowlist; missing or empty means the allowlist is empty).
set -euo pipefail
f=${OBS_ALLOWLIST_FILE:-/home/debian/obs-allowlist}
if [[ -s "$f" ]]; then
	GRAIN_GATE_OBSERVATION_TASKS=$(tr -d '[:space:]' <"$f")
	export GRAIN_GATE_OBSERVATION_TASKS
fi
exec "${REAL_GRAIN:-/home/debian/bin/grain}" "$@"
