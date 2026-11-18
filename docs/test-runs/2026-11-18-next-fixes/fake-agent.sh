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

# Fake agent CLI wrapper for the 2026-11-18 §8 walk: records the branch and
# base the run's checkout landed on (item 1's firing base), then hands the
# prompt to the 2026-11-13 harness's fake-agy.py.
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
prompt=$(cat)
task=$(grep -oE 'You are running as task [A-Za-z0-9_-]+' <<<"$prompt" | awk '{print $NF}')
if [[ -n "$task" && -n "${WALK_DIR:-}" ]]; then
	{
		echo "pwd=$PWD"
		for d in work .; do
			if git -C "$d" rev-parse --git-dir >/dev/null 2>&1; then
				echo "checkout=$d head=$(git -C "$d" rev-parse --abbrev-ref HEAD) upstream-base=$(git -C "$d" log -1 --format=%D HEAD)"
				break
			fi
		done
		echo "head-commit=$(git -C work rev-parse --short HEAD 2>/dev/null)"
	} >"$WALK_DIR/runs/checkout-$task.txt" 2>&1
fi
exec "$HERE/../2026-11-13-grain-health-exit-codes/fake-agy.py" "$@" <<<"$prompt"
