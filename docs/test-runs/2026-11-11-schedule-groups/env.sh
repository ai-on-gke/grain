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

# env.sh starts the §10 environment and leaves it running:
#   - githubsim_server (githubsim REST + git http-backend) on 127.0.0.1:4260
#     serving acme/widgets and acme/other;
#   - a real `grain daemon -sandbox-port-base=0` over SQLite on
#     127.0.0.1:4261 with host-sandboxed grain CI, the simulator as its
#     GitHub host and no model backend, so every filed task stays queued.
#
# acme/widgets declares one overlap group "train" across a task schedule
# (a-task), a suite schedule (b-suite) and a workflow schedule (c-workflow,
# grain-ci: true), all every 10 minutes (the floor for `every:`; step scripts pull next_run_at forward to fire them sooner), plus a workflow cron
# (.grain/workflows/live.yml, no grain-ci: true) that ci_mode: github
# drops at every firing. acme/other declares a task schedule in its own
# group "train".
#
# Source helpers.sh afterwards for `api`, `db` and friends.
set -euo pipefail

GRAIN_BIN=${GRAIN_BIN:-/home/debian/bin/grain}
SIM_BIN=${SIM_BIN:-/home/debian/bin/githubsim_server}
WORK_ROOT=${WORK_ROOT:-/var/tmp/walk-sched-groups}
SIM_PORT=${SIM_PORT:-4260}
UI_PORT=${UI_PORT:-4261}
SIM_URL="http://127.0.0.1:${SIM_PORT}"
API_URL="http://127.0.0.1:${UI_PORT}"

if [ -n "${KEEP:-}" ]; then
  [ -f "$WORK_ROOT/daemon.pid" ] && kill "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || true
  sleep 2
else
for f in daemon sim; do
  [ -f "$WORK_ROOT/$f.pid" ] && kill "$(cat "$WORK_ROOT/$f.pid")" 2>/dev/null || true
done
sleep 1
rm -rf "$WORK_ROOT"
mkdir -p "$WORK_ROOT/repos" "$WORK_ROOT/data" "$WORK_ROOT/sb" "$WORK_ROOT/clones" "$WORK_ROOT/out"

setsid "$SIM_BIN" -addr "127.0.0.1:${SIM_PORT}" -repos-dir "$WORK_ROOT/repos" \
  -repos "acme/widgets,acme/other" -token sim-token \
  >"$WORK_ROOT/sim.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/sim.pid"
for _ in $(seq 50); do curl -fsS "$SIM_URL/_sim/state" >/dev/null 2>&1 && break; sleep 0.2; done

init_repo() {
  local repo=$1 dir="$WORK_ROOT/clones/${1//\//_}"
  mkdir -p "$dir"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name "Alice Example"
  git -C "$dir" config user.email "alice@example.com"
  git -C "$dir" remote add origin "http://sim-token:@127.0.0.1:${SIM_PORT}/${repo}.git"
}

W="$WORK_ROOT/clones/acme_widgets"
init_repo acme/widgets
mkdir -p "$W/.grain/templates" "$W/.grain/suites" "$W/.grain/workflows" "$W/.grain/schedules"
echo pass >"$W/status.txt"
cat >"$W/.grain/templates/nightly.yml" <<'EOF'
title: Nightly widgets sweep
body: Sweep the widgets.
EOF
cat >"$W/.grain/suites/sweep.yml" <<'EOF'
templates: [nightly]
EOF
cat >"$W/.grain/workflows/check.yml" <<'EOF'
name: check
on:
  push:
  schedule:
    - cron: "17 3 * * *"
jobs:
  check:
    steps:
      - name: verify status
        run: |
          if [ -f slow.txt ]; then sleep "$(cat slow.txt)"; fi
          grep -qx pass status.txt
EOF
cat >"$W/.grain/workflows/live.yml" <<'EOF'
name: live
on:
  schedule:
    - cron: "11 2 * * *"
jobs:
  live:
    steps:
      - name: live
        run: echo live
EOF
cat >"$W/.grain/schedules/a-task.yml" <<'EOF'
template: nightly
every:
  minutes: 10
overlap: {skip: true, group: train}
EOF
cat >"$W/.grain/schedules/b-suite.yml" <<'EOF'
suite: sweep
base: main
every:
  minutes: 10
overlap-group: train
EOF
cat >"$W/.grain/schedules/c-workflow.yml" <<'EOF'
workflow: check
grain-ci: true
every:
  minutes: 10
overlap: {skip: true, group: train}
EOF
if [ -n "${LEGACY:-}" ]; then
  sed -i 's/^overlap: {skip: true, group: train}$/overlap: skip/; /^overlap-group: train$/d' "$W"/.grain/schedules/*.yml
fi
git -C "$W" add -A && git -C "$W" commit -q -m "acme/widgets: three schedules in overlap group train" && git -C "$W" push -q -u origin main

O="$WORK_ROOT/clones/acme_other"
init_repo acme/other
mkdir -p "$O/.grain/templates" "$O/.grain/schedules"
cat >"$O/.grain/templates/tidy.yml" <<'EOF'
title: Tidy other
body: Tidy acme/other.
EOF
cat >"$O/.grain/schedules/tidy.yml" <<'EOF'
template: tidy
every:
  minutes: 10
overlap: {skip: true, group: train}
EOF
[ -z "${LEGACY:-}" ] || sed -i 's/^overlap: {skip: true, group: train}$/overlap: skip/' "$O"/.grain/schedules/*.yml
git -C "$O" add -A && git -C "$O" commit -q -m "acme/other: same-named group" && git -C "$O" push -q -u origin main

cat >"$WORK_ROOT/fake-agy.sh" <<'EOF'
#!/usr/bin/env bash
for arg in "$@"; do case "$arg" in --help) exit 0;; --version) echo 'fake agy'; exit 0;; models) exit 0;; esac; done
exit 0
EOF
chmod +x "$WORK_ROOT/fake-agy.sh"
fi

${FAKETIME_PREFIX:-} setsid "$GRAIN_BIN" daemon -data-dir "$WORK_ROOT/data" -ui-addr "127.0.0.1:${UI_PORT}" \
  -sandbox-port-base=0 -host-sandboxes -sandbox-dir "$WORK_ROOT/sb" -ci -reconcile -poll-interval 1s \
  -github-host "127.0.0.1:${SIM_PORT}" -github-insecure-http -agy-path "$WORK_ROOT/fake-agy.sh" \
  >"$WORK_ROOT/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK_ROOT/daemon.pid"
for _ in $(seq 90); do
  kill -0 "$(cat "$WORK_ROOT/daemon.pid")" 2>/dev/null || { tail -n 40 "$WORK_ROOT/daemon.log"; exit 1; }
  if [ -s "$WORK_ROOT/data/local-api-token" ] &&
    curl -fsS -H "Authorization: Bearer $(cat "$WORK_ROOT/data/local-api-token")" "$API_URL/api/config" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
TOK=$(cat "$WORK_ROOT/data/local-api-token")
api() { curl -sS -X "$1" -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" ${3:+--data "$3"} "$API_URL$2"; }
api PUT /api/github-tokens/default '{"value":"sim-token"}' >/dev/null
# Nothing is dispatched (there is no model backend to run it anyway): every
# task a schedule files stays queued with no run, which is the
# never-started blocker the starved-firing rule is about. RESUME=1 skips it.
[ -n "${RESUME:-}" ] || api POST /api/pipeline-pause '{"reason":"schedule-group walkthrough: hold every filed task in the queue"}' >/dev/null
[ -n "${KEEP:-}" ] && { echo "daemon restarted: $API_URL"; exit 0; }
for r in acme/widgets acme/other; do
  api POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
  api PUT "/api/repos/${r%/*}/${r#*/}/ci-mode" '{"ciMode":"github"}' >/dev/null
  api PUT "/api/repos/${r%/*}/${r#*/}/declarations" '{"declarations":true}' >/dev/null
done
echo "environment up: $API_URL (token in $WORK_ROOT/data/local-api-token)"
