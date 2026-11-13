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

# Unit tests for scripts/release-train-actions.sh: what the release train
# asks of the Actions API -- Stage 1 on a new rc, the flake and infra
# re-runs, cancelling a dropped rc's runs -- against a fake curl that
# records every call and answers GETs from a fixture, in a throwaway
# repository with the rc tags. tests/deploy/release_trunk_scripts_test.go
# runs this in CI; by hand:
#
#   bash scripts/release-train-actions_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

actions="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-train-actions.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

# The fake curl: every call is a line "<METHOD> <url> <body>" in
# $work/calls, and a GET writes $work/runs.json to its -o file.
mkdir -p "$work/bin"
cat > "$work/bin/curl" <<'EOF'
#!/usr/bin/env bash
method=GET body="" out="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -d) body="$2"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    -H | --max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "$method $url $body" >> "$FAKE_CALLS"
if [ "$method" = GET ] && [ -n "$out" ]; then cp "$FAKE_RUNS" "$out"; fi
EOF
chmod +x "$work/bin/curl"
export PATH="$work/bin:$PATH" FAKE_CALLS="$work/calls" FAKE_RUNS="$work/runs.json"
export GITHUB_TOKEN=fake RELEASE_GITHUB_REPO=example/grain RELEASE_GITHUB_API_URL=https://api.example.test
unset DRY_RUN RELEASE_TAGS_FIRE

git init -q "$work/repo"
git -C "$work/repo" commit -q --allow-empty -m "feat: one"
git -C "$work/repo" tag -a v3.2.0-rc.1 -m rc1
git -C "$work/repo" commit -q --allow-empty -m "fix: two"
git -C "$work/repo" tag -a v3.2.0-rc.2 -m rc2
rc1="$(git -C "$work/repo" rev-parse 'v3.2.0-rc.1^{commit}')"

failures=0
cases=0
out=""
status=0
calls=""

# run <train output lines...>: the script's output in $out, its exit status
# in $status and the API calls it made in $calls.
run() {
  printf '%s\n' "$@" > "$work/train.out"
  : > "$work/calls"
  status=0
  out="$(cd "$work/repo" && bash "$actions" "$work/train.out" 2>&1)" || status=$?
  calls="$(cat "$work/calls")"
}

runs() { # runs <head_branch>:<status>:<id>...: the fixture's nightly.yml runs
  local r
  for r in "$@"; do
    IFS=: read -r b s id <<< "$r"
    jq -cn --arg b "$b" --arg s "$s" --argjson id "$id" '{id: $id, head_branch: $b, status: $s}'
  done | jq -s '{workflow_runs: .}' > "$work/runs.json"
}

check() { # check <case> <want-status> <want-substring-of-output-or-calls>...
  local name="$1" want="$2" s
  shift 2
  cases=$((cases + 1))
  if [ "$status" != "$want" ]; then
    echo "FAIL $name: exit $status, want $want"
    printf '%s\n%s\n' "$out" "$calls" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  for s in "$@"; do
    if [[ "$out$calls" != *"$s"* ]]; then
      echo "FAIL $name: output and calls lack \"$s\""
      printf '%s\n%s\n' "$out" "$calls" | sed 's/^/    /'
      failures=$((failures + 1))
      return
    fi
  done
  echo "ok   $name"
}

lacks() { # lacks <case> <substring>
  cases=$((cases + 1))
  if [[ "$out$calls" == *"$2"* ]]; then
    echo "FAIL $1: output or calls have \"$2\""
    printf '%s\n%s\n' "$out" "$calls" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  echo "ok   $1"
}

api=https://api.example.test/repos/example/grain

runs
run ""
check "an empty output asks for nothing" 0 "the train asked for nothing"
lacks "an empty output calls nothing" "GET "

# A new rc pushed with GITHUB_TOKEN fires no workflow: Stage 1 is dispatched.
run "rc_tag=v3.2.0-rc.2"
check "a new rc starts Stage 1 by dispatch" 0 \
  "POST $api/actions/workflows/nightly.yml/dispatches {\"ref\":\"v3.2.0-rc.2\",\"inputs\":{\"jobs\":\"\"}}"

RELEASE_TAGS_FIRE=true run "rc_tag=v3.2.0-rc.2"
check "a new rc pushed with a firing token starts Stage 1 itself" 0 "push trigger starts Stage 1"
lacks "a new rc pushed with a firing token is not dispatched again" "POST "

# Re-runs: the nightly jobs together, once Stage 1 on the rc has finished.
runs "v3.2.0-rc.1:completed:11" "main:in_progress:12"
run "qualification=pending" "qualifying_rc=v3.2.0-rc.1" \
  "qualification_rerun=nightly:go-long:flake,nightly:vulns:infra,qualification:grain/release-qualification:infra"
check "the flake retry and infra re-run go out as one dispatch" 0 \
  "GET $api/actions/workflows/nightly.yml/runs?head_sha=$rc1" \
  "POST $api/actions/workflows/nightly.yml/dispatches {\"ref\":\"v3.2.0-rc.1\",\"inputs\":{\"jobs\":\"go-long,vulns\"}}" \
  "is the deployment's to re-run"

runs "v3.2.0-rc.1:completed:11"
run "qualification=infra" "qualifying_rc=v3.2.0-rc.1" \
  "qualification_rerun=nightly:grant-matrix-ui:infra"
check "optional nightly job grant-matrix-ui dispatches by its job id" 0 \
  "POST $api/actions/workflows/nightly.yml/dispatches {\"ref\":\"v3.2.0-rc.1\",\"inputs\":{\"jobs\":\"grant-matrix-ui\"}}"

# idle-gate runs on every dispatch, so a dispatch naming it is its retry
# (candidate-gate.sh judges it as an optional job), not a refusal.
runs "v3.2.0-rc.1:completed:11"
run "qualification=infra" "qualifying_rc=v3.2.0-rc.1" \
  "qualification_rerun=nightly:idle-gate:infra"
check "a cancelled idle-gate re-runs by its job id" 0 \
  "POST $api/actions/workflows/nightly.yml/dispatches {\"ref\":\"v3.2.0-rc.1\",\"inputs\":{\"jobs\":\"idle-gate\"}}"
lacks "an idle-gate re-run is not refused" "::error::"

# A job known only by display name is not a key in nightly.yml: refuse
# loudly rather than dispatch a run in which every job is skipped.
runs "v3.2.0-rc.1:completed:11"
run "qualification=pending" "qualifying_rc=v3.2.0-rc.1" \
  "qualification_rerun=nightly:grant matrix through the UI:flake"
check "a rerun naming a display name instead of a nightly.yml job key is refused" 1 \
  "WARNING: v3.2.0-rc.1: 'grant matrix through the UI' is not a dispatchable job key in nightly.yml" \
  "::error::release-train: v3.2.0-rc.1 asked to re-run 'grant matrix through the UI', which is not a job key in nightly.yml"
lacks "a refused display-name rerun dispatches nothing" "POST "

runs "v3.2.0-rc.1:completed:11"
run "qualification=pending" "qualifying_rc=v3.2.0-rc.1" \
  "qualification_rerun=nightly:ui e2e, repeated:flake"
check "a display name containing a comma is parsed whole and refused" 1 \
  "WARNING: v3.2.0-rc.1: 'ui e2e, repeated' is not a dispatchable job key in nightly.yml"
lacks "a comma display-name rerun dispatches nothing" "POST "

runs "v3.2.0-rc.1:completed:11"
run "qualification=pending" "qualifying_rc=v3.2.0-rc.1" \
  "qualification_rerun=nightly:go-long:flake,nightly:grant matrix through the UI:flake"
check "a valid job still dispatches while an unmapped display name fails loudly" 1 \
  "WARNING: v3.2.0-rc.1: 'grant matrix through the UI' is not a dispatchable job key in nightly.yml" \
  "POST $api/actions/workflows/nightly.yml/dispatches {\"ref\":\"v3.2.0-rc.1\",\"inputs\":{\"jobs\":\"go-long\"}}"

runs "v3.2.0-rc.1:in_progress:21"
run "qualifying_rc=v3.2.0-rc.1" "qualification_rerun=nightly:go-long:flake"
check "a re-run waits while Stage 1 still runs on the rc" 0 "re-running go-long waits for it"
lacks "a re-run while Stage 1 runs dispatches nothing" "POST "

runs
run "qualifying_rc=v3.2.0-rc.1" "qualification_rerun=qualification:grain/release-qualification:flake"
check "a Stage 2 re-run is the deployment's" 0 "is the deployment's to re-run"
lacks "a Stage 2 re-run calls nothing" "POST "

# A dropped rc: its own runs are cancelled, never main's on the same commit.
runs "v3.2.0-rc.1:in_progress:31" "v3.2.0-rc.1:queued:32" "v3.2.0-rc.1:completed:33" "main:in_progress:34"
run "discarded_rc=v3.2.0-rc.1" "qualification=fail" "rc_tag=v3.2.0-rc.2"
check "a dropped rc's open runs are cancelled" 0 \
  "POST $api/actions/runs/31/cancel" "POST $api/actions/runs/32/cancel" \
  "POST $api/actions/workflows/nightly.yml/dispatches {\"ref\":\"v3.2.0-rc.2\""
lacks "a dropped rc's finished run is left alone" "runs/33/cancel"
lacks "main's run on the same commit is left alone" "runs/34/cancel"

run "discarded_rc=v9.9.9-rc.1"
check "a dropped rc with no tag here is an error" 1 "no tag v9.9.9-rc.1"

# A dry run reads but changes nothing.
runs "v3.2.0-rc.1:in_progress:41"
DRY_RUN=true run "discarded_rc=v3.2.0-rc.1" "rc_tag=v3.2.0-rc.2"
check "a dry run says what it would do" 0 "[dry-run] would POST actions/runs/41/cancel" \
  "[dry-run] would POST actions/workflows/nightly.yml/dispatches"
lacks "a dry run posts nothing" "POST https://"

# Without a token nothing is called and the run says so.
GITHUB_TOKEN="" run "rc_tag=v3.2.0-rc.2"
check "no token leaves Stage 1 undone, loudly" 1 "no GITHUB_TOKEN"

run "escalated_version=v3.2.0" "qualification_stalled=v3.2.0-rc.1"
check "escalation and stalls are reported" 0 "::error::release-train: v3.2.0 has used RELEASE_MAX_RC_ATTEMPTS" \
  "::warning::release-train: v3.2.0-rc.1"

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures of $cases cases failed"
  exit 1
fi
echo "all $cases cases passed"
