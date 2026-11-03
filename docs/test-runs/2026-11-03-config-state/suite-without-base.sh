#!/bin/sh
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

# Item 4: on `grain demo -github-sim`, commit a suite and a schedule that
# runs it with no `base:` to acme/widgets's simulated bare repository, then
# read every place the repository's declaration state is reported.
#   DEMO_DIR: the demo's temp dir (holds githubsim/acme/widgets.git and
#   local-api-token); PORT: the demo's -addr port.
set -u
DEMO_DIR=${DEMO_DIR:?}
PORT=${PORT:-18742}
T=$(cat "$DEMO_DIR/local-api-token")
W=$(mktemp -d)
git clone -q "file://$DEMO_DIR/githubsim/acme/widgets.git" "$W/w"
cd "$W/w" || exit 1
mkdir -p .grain/templates .grain/suites .grain/schedules
cat > .grain/templates/nightly.yml <<'EOF'
title: Nightly sweep task
body: Sweep the repository.
EOF
cat > .grain/suites/sweep.yml <<'EOF'
name: Nightly sweep
templates: [nightly]
EOF
cat > .grain/schedules/sweep.yml <<'EOF'
name: Sweep every night
suite: sweep
daily:
  at: "03:00"
EOF
git add .grain && git -c user.name=walk -c user.email=walk@example.com commit -qm 'declare a baseless suite schedule'
git push -q origin HEAD
echo "pushed $(git rev-parse --short HEAD)"
sleep 12
api() { curl -s -H "Authorization: Bearer $T" "http://127.0.0.1:$PORT$1"; }
echo "== /api/repos/acme/widgets/declarations"; api /api/repos/acme/widgets/declarations | jq -c . | cut -c1-1200
echo "== /api/repos (acme/widgets)"; api /api/repos | jq -c '(.[]? // .repos[]?) | select(.repo=="acme/widgets" or .name=="acme/widgets")' | cut -c1-1500
