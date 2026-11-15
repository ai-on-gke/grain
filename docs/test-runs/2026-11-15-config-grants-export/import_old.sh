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

# import_old.sh VERSION CONFIG_GIT: point a fresh SQLite installation of
# /home/debian/bin/grain-VERSION at the bare grain/config CONFIG_GIT in
# read-only IaC mode, pull and apply it, and print the four repository
# access tables.
set -uo pipefail
source "$(dirname "$0")/lib.sh"
v=$1 remote=$2
G=/home/debian/bin/grain-$v
D=/tmp/walk-import-$v
rm -rf "$D" && mkdir -p "$D/data" "$D/sandboxes" "$D/home"
cp /tmp/walk-cfg/gemini.key "$D/"
"$G" state -data-dir "$D/data" repo -branch main "file://$remote"; echo "repo rc=$?"
# The daemon creates the store and, in IaC mode, pulls and applies the
# repository on start; `state sync` then pulls and applies once more.
GRAIN=$G start_daemon "$D" || tail -20 "$D/daemon.log"
sleep 5
stop_daemon "$D"
"$G" state -data-dir "$D/data" sync; echo "sync rc=$?"
grep -i -E 'state|config repo|import|refus' "$D/daemon.log" | grep -v -i 'debug' | head -15
dump_access "$D/data/store/grain.db"
