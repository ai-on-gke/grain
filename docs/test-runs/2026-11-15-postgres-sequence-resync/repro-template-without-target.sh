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

# Finding (outside §3's sequence code): a template added to grain/config's
# settings/templates.yml without a `target:` key is refused on import with
# SQLSTATE 23502 (template.target_owner is NOT NULL with no default), and the
# daemon retries the unapplied commit on every sync. decl.go's own rule is
# that a key a settings file leaves out lands on the column's default.
PG_DB=${PG_DB:-grain_repro}
DAEMON_PORT=${DAEMON_PORT:-4292}
WORK_DIR=${WORK_DIR:-/home/debian/walk/repro}
source "$(dirname "$0")/env.sh"
reset_db
reset_work
start_daemon "$GRAIN"
repo="$WORK_DIR/data/state-repo"
cat >>"$repo/settings/templates.yml" <<'YML'
templates:
  - id: template-1
    name: minimal
    title: A minimal hand-written template
    body: written in grain/config by hand
YML
git -C "$repo" -c user.name="Walk" -c user.email="walk@example.com" add -A
git -C "$repo" -c user.name="Walk" -c user.email="walk@example.com" commit -qm "add a minimal template"
for _ in $(seq 40); do
	grep -q "could not apply" "$WORK_DIR/daemon.log" && break
	sleep 1
done
grep -oE "state sync failed: .*" "$WORK_DIR/daemon.log" | head -1 | cut -c1-400 || true
echo "template rows: $(psql_db -c 'SELECT count(*) FROM template')"
stop_daemon
