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

# run_versions.sh: §6 items 1 and 3. Seeds the templates and schedule on
# v3.8.0 over SQLite (its export commit is the "before"), lets HEAD
# export the same store (the upgrade diff), clones that grain/config to a
# bare remote, and imports it into fresh v3.1.1, v3.5.1 and v3.8.0
# installations (import_old.sh). Binaries: /home/debian/bin/grain-<v>,
# built from release/v3.1.1, release/v3.5.1, v3.8.0 and HEAD.
set -euo pipefail
GRAIN=/home/debian/bin/grain-v3.8.0
source "$(dirname "$0")/lib.sh"
W=/tmp/walk-cfg
out=$HERE15/captures
init_env "$W"
start_ghsim
trap 'stop_env "$W"' EXIT
start_daemon "$W"
seed_access >/dev/null
sleep 3
stop_daemon "$W"
before=$(git -C "$W/data/state-repo" rev-parse HEAD)
/home/debian/bin/grain-head state -data-dir "$W/data" sync
git -C "$W/data/state-repo" diff "$before" HEAD >"$out/v3.8.0-to-head-upgrade.diff"
git -C "$W/data/state-repo" diff --numstat "$before" HEAD
rm -rf "$W/config.git" && git clone -q --bare "$W/data/state-repo" "$W/config.git"
stop_env "$W"
for v in v3.1.1 v3.5.1 v3.8.0; do
	"$HERE15/import_old.sh" "$v" "$W/config.git" >"$W/import-$v.txt" 2>&1 || true
	{ grep -E 'repo rc|sync rc|applied|newer build' "$W/import-$v.txt" | sed 's/^[0-9/]* [0-9:]* //'
	  sed -n '/^-- template_read/,$p' "$W/import-$v.txt"; } >"$out/import-head-into-$v.txt"
done
