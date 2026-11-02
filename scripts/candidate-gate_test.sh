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

# Unit tests for scripts/candidate-gate.sh's select-lkg and check-soak,
# against a throwaway repository and JSON shaped the way grain's API
# answers (GET /api/ci/runs, /api/persistent-sandboxes, /api/tasks).
# tests/deploy/release_trunk_scripts_test.go runs this in CI; by hand:
#
#   bash scripts/candidate-gate_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

gate="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/candidate-gate.sh"
work="$(mktemp -d)"
server_pid=""
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

failures=0
cases=0
out=""
status=0

# run <args...>: the gate's output in $out and its exit status in $status.
run() {
  status=0
  out="$(cd "$work/repo" && bash "$gate" "$@" 2>&1)" || status=$?
}

check() { # check <case> <want-status> <want-substring>...
  local name="$1" want="$2" s
  shift 2
  cases=$((cases + 1))
  if [ "$status" != "$want" ]; then
    echo "FAIL $name: exit $status, want $want"
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  for s in "$@"; do
    if [[ "$out" != *"$s"* ]]; then
      echo "FAIL $name: output lacks \"$s\""
      printf '%s\n' "$out" | sed 's/^/    /'
      failures=$((failures + 1))
      return
    fi
  done
  echo "ok   $name"
}

lacks() { # lacks <case> <substring>
  cases=$((cases + 1))
  if [[ "$out" == *"$2"* ]]; then
    echo "FAIL $1: output has \"$2\""
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  echo "ok   $1"
}

iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%S.123456789Z; }

# The repository: main's first-parent line c1..c5, each commit a merge's
# result, with c0 the floor (the previous rc).
git init -q -b main "$work/repo"
cd "$work/repo"
c=()
for i in 0 1 2 3 4 5; do
  git commit -q --allow-empty -m "c$i"
  c[i]="$(git rev-parse HEAD)"
done
c0="${c[0]}" c1="${c[1]}" c2="${c[2]}" c3="${c[3]}" c4="${c[4]}" c5="${c[5]}"
git update-ref refs/remotes/origin/main "$c5"
cd - >/dev/null

now=1790000000
recent=$((now - 3600))
stale=$((now - 30 * 3600))

# run_json <commit> <workflow> <conclusion> [<started-epoch>] [<job>]: one
# run with one job.
run_json() {
  local t="${4:-$recent}"
  printf '{"id":"r-%s-%s-%s","repo":"o/r","ref":"main","commit":"%s","event":"push","startedAt":"%s","finishedAt":"%s","conclusion":"%s","jobs":[{"workflow":"%s","workflowPath":".grain/workflows/%s.yml","jobId":"%s","name":"%s","startedAt":"%s","finishedAt":"%s","conclusion":"%s"}]}' \
    "$1" "$2" "$t" "$1" "$(iso "$t")" "$(iso $((t + 600)))" "$3" "$2" "$2" "${5:-$2-job}" "${5:-$2-job}" "$(iso "$t")" "$(iso $((t + 600)))" "$3"
}

runs_file() { # runs_file <name> <run-json>...: a GET /api/ci/runs answer
  local f="$work/$1.json" first=1 r
  {
    printf '{"enabled":true,"runs":['
    for r in "${@:2}"; do
      [ $first = 1 ] || printf ','
      first=0
      printf '%s' "$r"
    done
    printf ']}'
  } > "$f"
  echo "$f"
}

green() { # all three required suites green on $1
  run_json "$1" tests success "${2:-$recent}"
  echo
  run_json "$1" release success "${2:-$recent}"
  echo
  run_json "$1" nightly success "${2:-$recent}"
}

echo '[]' > "$work/no-tasks.json"

# --- select-lkg ---

mapfile -t r < <(green "$c3")
f="$(runs_file exact "${r[@]}" "$(run_json "$c5" tests success)" "$(run_json "$c5" release failure)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: the newest commit with all three suites green on its own sha" 0 "LKG $c3" "${c5:0:12}: tests.yml success release.yml failure; not known-good."

f="$(runs_file floor "${r[@]}")"
run select-lkg --runs-json "$f" --floor-sha "$c3" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: nothing at or before the floor qualifies" 1 "LKG none"

f="$(runs_file ff "$(run_json "$c2" nightly success)" "$(run_json "$c4" tests success)" "$(run_json "$c4" release success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a daytime commit rides an ancestor's fresh nightly" 0 "LKG $c4" "nightly.yml passed on its ancestor ${c2:0:12}"

run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now"
check "select-lkg: no fast-forward without a task list to rule out a repair" 1 "LKG none" "no task list"

cat > "$work/repair.json" <<EOF
[{"id":"901","title":"fix main","state":"running","repo":"o/r","base":"main","fixesBaseBranch":true},
 {"id":"902","title":"old fix","state":"completed","repo":"o/r","base":"main","fixesBaseBranch":true}]
EOF
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/repair.json"
check "select-lkg: an open repair of main stops the fast-forward" 1 "LKG none" "a repair of main is open (task 901)"

f="$(runs_file stale "$(run_json "$c2" nightly success "$stale")" "$(run_json "$c4" tests success)" "$(run_json "$c4" release success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: an ancestor's nightly older than 24h vouches for nothing" 1 "LKG none" "more than 24h"

f="$(runs_file newerred "$(run_json "$c2" nightly success)" "$(run_json "$c3" nightly failure)" "$(run_json "$c4" tests success)" "$(run_json "$c4" release success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: only the newest nightly on the ancestry counts" 1 "LKG none" "is failure"

f="$(runs_file recheck "$(run_json "$c4" tests failure "$((recent - 900))" go)" "$(run_json "$c4" tests success "$recent" go)" \
  "$(run_json "$c4" release success)" "$(run_json "$c4" nightly cancelled "$((recent - 900))")" "$(run_json "$c4" nightly success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: each job's latest result wins, so a recheck turns a suite green" 0 "LKG $c4"

f="$(runs_file infra "$(run_json "$c5" tests success)" "$(run_json "$c5" release success)" "$(run_json "$c5" nightly cancelled)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a cancelled required suite is infra, reported for a re-run" 1 "LKG none" "infra: nightly.yml at ${c5:0:12} was cancelled or errored"

mapfile -t r < <(green "$c5")
f="$(runs_file advisory "${r[@]}" "$(run_json "$c5" gcp-smoke failure)" "$(run_json "$c4" live-agent errored)" "$(run_json "$c5" post-reset-e2e failure)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: advisory suites are reported and never block" 0 "LKG $c5" \
  "advisory gcp-smoke.yml: failure at ${c5:0:12} (does not block)" \
  "advisory live-agent.yml: infra at ${c4:0:12} (does not block)" \
  "advisory post-reset-e2e.yml: failure at ${c5:0:12}"

f="$(runs_file pending "$(run_json "$c5" tests "")" "$(run_json "$c5" release success)" "$(run_json "$c5" nightly success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a suite still running is not green" 1 "tests.yml pending"

echo '{"enabled":true,"runs":[]}' > "$work/empty.json"
run select-lkg --runs-json "$work/empty.json" --now "$now"
check "select-lkg: no runs, no candidate" 1 "LKG none" "tests.yml missing"

run select-lkg
check "select-lkg: --runs-json is required" 64 "usage:"

# gh_run <commit> <workflow> <conclusion|in_progress> [<started-epoch>] [<event>] [<head-repo>]:
# one GitHub Actions workflow run, as GET /repos/{owner}/{repo}/actions/runs lists it.
gh_run() {
  local t="${4:-$recent}" status=completed conclusion="$3"
  if [ "$3" = in_progress ]; then status=in_progress conclusion=null; else conclusion="\"$3\""; fi
  printf '{"id":%s,"name":"%s","path":".github/workflows/%s.yml","head_branch":"main","head_sha":"%s","event":"%s","status":"%s","conclusion":%s,"run_attempt":1,"created_at":"%s","run_started_at":"%s","updated_at":"%s","repository":{"full_name":"o/r"},"head_repository":{"full_name":"%s"}}' \
    "$RANDOM" "$2" "$2" "$1" "${5:-push}" "$status" "$conclusion" "$(date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ)" "$(date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ)" "$(date -u -d "@$((t + 600))" +%Y-%m-%dT%H:%M:%SZ)" "${6:-o/r}"
}
gh_file() { # gh_file <name> <run-json>...: a GET .../actions/runs answer
  local f="$work/$1.json" IFS=,
  printf '{"total_count":%s,"workflow_runs":[%s]}' "$(($# - 1))" "${*:2}" > "$f"
  echo "$f"
}

f="$(gh_file gh-exact "$(gh_run "$c3" tests success)" "$(gh_run "$c3" release success)" "$(gh_run "$c3" nightly success "$recent" schedule)" \
  "$(gh_run "$c5" tests success)" "$(gh_run "$c5" release failure)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): workflow runs are read by their workflow file" 0 "LKG $c3" "${c5:0:12}: tests.yml success release.yml failure; not known-good."

f="$(gh_file gh-ff "$(gh_run "$c2" nightly success "$recent" schedule)" "$(gh_run "$c4" tests success)" "$(gh_run "$c4" release success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): a scheduled nightly vouches for the commits after it" 0 "LKG $c4" "nightly.yml passed on its ancestor ${c2:0:12}"

f="$(gh_file gh-pr "$(gh_run "$c5" tests success "$recent" pull_request)" "$(gh_run "$c5" release success)" "$(gh_run "$c5" nightly success)" \
  "$(gh_run "$c4" tests success "$recent" push someone/fork)" "$(gh_run "$c4" release success)" "$(gh_run "$c4" nightly success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): a pull request's or a fork's run vouches for nothing" 1 "LKG none" \
  "${c5:0:12}: tests.yml missing" "${c4:0:12}: tests.yml missing"

f="$(gh_file gh-rerun "$(gh_run "$c4" tests failure "$((recent - 900))")" "$(gh_run "$c4" tests success)" "$(gh_run "$c4" release success)" \
  "$(gh_run "$c4" nightly success)" "$(gh_run "$c5" tests in_progress)" "$(gh_run "$c5" release success)" "$(gh_run "$c5" nightly timed_out)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): the newest run wins; in progress is pending, timed out is infra" 0 "LKG $c4" \
  "${c5:0:12}: tests.yml pending" "infra: nightly.yml at ${c5:0:12} was cancelled or errored"

# --- check-soak ---

rc="$c3"
deployed=$((now - 23 * 3600))
sandbox() { # sandbox <state> <commit> [<events-json>]
  cat > "$work/sandboxes.json" <<EOF
[{"name":"other","state":"running","commit":"$c1","restarts":0},
 {"name":"grain-candidate","repo":"o/r","ref":"candidate","state":"$1","commit":"$2","restarts":4,
  "lastRefresh":{"at":"$(iso "$deployed")","from":"$c1","to":"$rc","reason":"candidate moved"},
  "events":${3:-[]}}]
EOF
}
tasks() { # tasks <json>
  printf '%s\n' "$1" > "$work/tasks.json"
}
explore_done='[{"id":"700","title":"Daily exploratory UI testing and defect discovery on staging","state":"completed","repo":"o/r","createdAt":"'"$(iso $((deployed + 60)))"'","startedAt":"'"$(iso $((deployed + 120)))"'"}]'

# The staging server: a directory served over HTTP, one file per path.
mkdir -p "$work/www/api"
cat > "$work/www/index.html" <<'EOF'
<!doctype html><html><body><div id="root"></div></body></html>
EOF
config() { printf '{"version":{"release":"%s","commit":"%s"}}\n' "$1" "$2" > "$work/www/api/config"; }
config v2.3.0-rc.2 "$rc"
echo '[]' > "$work/www/api/tasks"
echo '[]' > "$work/www/api/persistent-sandboxes"
staging=""
if command -v python3 >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
  port=$((20000 + RANDOM % 20000))
  (cd "$work/www" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
  server_pid=$!
  for _ in $(seq 50); do
    curl -s -o /dev/null "http://127.0.0.1:$port/" && break
    sleep 0.1
  done
  staging="http://127.0.0.1:$port"
else
  echo "skip the HTTP probe's cases: python3 or curl is not installed"
fi

soak() {
  run check-soak --rc-tag v2.3.0-rc.2 --rc-sha "$rc" --sandbox-json "$work/sandboxes.json" \
    --tasks-json "$work/tasks.json" --staging-url "${staging:-http://127.0.0.1:1}"
}
export RELEASE_NEVER_PROMOTE="v1.14.0"

if [ -n "$staging" ]; then
  sandbox running "$rc" '[{"at":"'"$(iso $((deployed - 60)))"'","kind":"exit","message":"before the refresh"},{"at":"'"$(iso "$deployed")"'","kind":"refresh","message":"onto rc"}]'
  tasks "$explore_done"
  soak
  check "check-soak: every signal green passes, whatever restarts counted before the deployment" 0 \
    "PASS supervisor:" "PASS http:" "PASS exploration: task 700" "PASS defects:" "PASS never:" "Soak gate: PASS v2.3.0-rc.2."

  sandbox running "$c1"
  soak
  check "check-soak: staging rolled back off the rc fails" 1 "FAIL supervisor: grain-candidate is running ${c1:0:12}, not ${rc:0:12}"

  sandbox restarting "$rc"
  soak
  check "check-soak: staging not running fails" 1 "FAIL supervisor: grain-candidate is restarting"

  sandbox running "$rc" '[{"at":"'"$(iso $((deployed + 300)))"'","kind":"refresh-crash","message":"exited 3 times"}]'
  soak
  check "check-soak: a refresh-crash since the deployment fails" 1 "FAIL supervisor:" "refresh-crash (exited 3 times)"

  sandbox running "$rc" '[{"at":"'"$(iso $((deployed + 300)))"'","kind":"exit","message":"status 2"}]'
  soak
  check "check-soak: the command exiting since the deployment fails" 1 "exited 1 time(s)"

  sandbox running "$rc"
  config v2.3.0-dev.4+abc "$c1"
  soak
  check "check-soak: /api/config reporting another version fails" 1 "FAIL http:" "reports version v2.3.0-dev.4+abc"
  config "" "$rc"
  soak
  check "check-soak: /api/config's commit stands in for the tag" 0 "PASS http:"
  config v2.3.0-rc.2 "$rc"

  rm "$work/www/api/tasks"
  soak
  check "check-soak: an endpoint answering 404 fails" 1 "/api/tasks answered 404"
  echo '[]' > "$work/www/api/tasks"

  tasks '[]'
  soak
  check "check-soak: no exploration run yet is pending" 2 "PENDING exploration:" "Soak gate: PENDING"

  tasks '[{"id":"690","title":"Daily exploratory UI testing and defect discovery on staging","state":"completed","startedAt":"'"$(iso $((deployed - 3600)))"'","createdAt":"'"$(iso $((deployed - 3600)))"'"}]'
  soak
  check "check-soak: an exploration run from before the deployment does not count" 2 "PENDING exploration: no staging-ui-exploration run"

  tasks '[{"id":"701","title":"Daily exploratory UI testing and defect discovery on staging","state":"running","startedAt":"'"$(iso $((deployed + 120)))"'"}]'
  soak
  check "check-soak: an exploration run still going is pending" 2 "PENDING exploration: task 701 is still running"

  tasks '[{"id":"702","title":"Daily exploratory UI testing and defect discovery on staging","state":"failed","startedAt":"'"$(iso $((deployed + 120)))"'"}]'
  soak
  check "check-soak: a failed exploration run fails" 1 "FAIL exploration: task 702"

  tasks "$(jq -c '. + [{"id":"710","title":"Upgrade pane clips on mobile","state":"queued","generatedFrom":"700","createdAt":"'"$(iso $((deployed + 900)))"'"},
                      {"id":"711","title":"Closed one","state":"closed","generatedFrom":"700"}]' <<< "$explore_done")"
  soak
  check "check-soak: an open defect filed by the exploration run fails" 1 "FAIL defects:" "task 710 (Upgrade pane clips on mobile)"
  lacks "check-soak: a closed defect is not listed" "task 711"

  tasks "$(jq -c '. + [{"id":"720","title":"rc-blocker: login loops","state":"proposed","createdAt":"'"$(iso $((deployed + 900)))"'"},
                      {"id":"721","title":"v2.3.0-rc.2 shows a blank page","state":"queued","createdAt":"'"$(iso $((deployed + 900)))"'"},
                      {"id":"722","title":"v2.3.0-rc.20 is not this one","state":"queued","createdAt":"'"$(iso $((deployed + 900)))"'"}]' <<< "$explore_done")"
  soak
  check "check-soak: tasks marked as blocking the rc fail it" 1 "task 720" "task 721"

  tasks "$explore_done"
  RELEASE_NEVER_PROMOTE="v1.14.0 v2.3.0-rc.2" soak
  check "check-soak: RELEASE_NEVER_PROMOTE naming the rc fails" 1 "FAIL never:" "names v2.3.0-rc.2"
  RELEASE_NEVER_PROMOTE="v2.3.0" soak
  check "check-soak: RELEASE_NEVER_PROMOTE naming the release fails" 1 "names v2.3.0"

  sandbox running "$c1"
  tasks '[]'
  soak
  check "check-soak: a FAIL wins over a PENDING" 1 "PENDING exploration:" "Soak gate: FAIL v2.3.0-rc.2 (supervisor)"
fi

sandbox running "$rc"
tasks "$explore_done"
run check-soak --rc-tag v2.3.0-rc.2 --rc-sha "$rc" --sandbox-json "$work/sandboxes.json" --tasks-json "$work/tasks.json" --staging-url http://127.0.0.1:1
check "check-soak: staging not answering fails" 1 "FAIL http: http://127.0.0.1:1: / answered 000"

echo '[]' > "$work/nosandbox.json"
run check-soak --rc-tag v2.3.0-rc.2 --rc-sha "$rc" --sandbox-json "$work/nosandbox.json" --tasks-json "$work/tasks.json" --staging-url http://127.0.0.1:1
check "check-soak: grain-candidate missing from the listing fails" 1 "FAIL supervisor: grain-candidate is not in the persistent sandbox listing"

run check-soak --rc-tag v2.3.0 --rc-sha "$rc" --sandbox-json "$work/sandboxes.json" --tasks-json "$work/tasks.json"
check "check-soak: a tag that is not an rc is a usage error" 64 "not a release candidate tag"

echo
echo "$((cases - failures))/$cases passed"
[ "$failures" -eq 0 ]
