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

# One tick of the continuous release train, as a grain task runs it
# (.grain/templates/release-train.yml, fired by
# .grain/schedules/release-train.yml; design/playbook/releases.playbook.md
# section 0.4). Everything the task used to do by hand is here, so a tick
# is one command and most ticks -- nothing to cut, an rc still qualifying --
# end at once:
#
#   1. a scratch clone of the task's remote (never the task checkout, which
#      the tags would be left behind in);
#   2. the CI verdicts, written unedited from grain's own forge connection
#      in the GitHub shapes scripts/candidate-gate.sh reads: main's
#      workflow runs, and for the rc in qualification its nightly.yml jobs
#      (Stage 1), its grain/release-qualification check runs (Stage 2) and,
#      for a dropped rc, its grain/rc-hold check runs;
#   3. scripts/release-from-trunk.sh in train mode under the train's own
#      tag grant (RELEASE_PUSH_GRANT=train), publishers off: images and the
#      GitHub Release are CI's, on the tags this pushes.
#
# The last line it prints is the tick's result for the task's closing note:
#
#   RELEASE_TRAIN_RESULT=noop    nothing was pushed (the reason is above)
#   RELEASE_TRAIN_RESULT=acted   it pushed a ref; the outputs above say which
#   RELEASE_TRAIN_RESULT=failed  it stopped with an error; report it
#
# Settings (environment): RELEASE_TASK_CHECKOUT (~/work), RELEASE_TASK_DIR
# (~/train), RELEASE_TASK_REPO (ai-on-gke/grain), GRAIN (grain), and
# anything scripts/release-from-trunk.sh reads, DRY_RUN among them.
set -uo pipefail

checkout="${RELEASE_TASK_CHECKOUT:-$HOME/work}"
dir="${RELEASE_TASK_DIR:-$HOME/train}"
repo="${RELEASE_TASK_REPO:-ai-on-gke/grain}"
grain="${GRAIN:-grain}"
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT

say() { echo "release-train-task: $*"; }
fail() {
  say "$*"
  echo "RELEASE_TRAIN_RESULT=failed"
  exit 1
}

# read_ci <dest> <what> <grain ci args...>: run `grain ci <args>` into
# <dest>. On failure, say which command failed, its exit status and what
# grain wrote to stderr -- the reason, which is otherwise lost behind the
# one-line fail -- and, for a capability refusal, which grant is missing,
# then fail with <what>.
read_ci() {
  local dest="$1" what="$2" status=0
  shift 2
  "$grain" ci "$@" >"$dest" 2>"$out/ci.err" || status=$?
  if [ "$status" = 0 ]; then
    cat "$out/ci.err" >&2
    return 0
  fi
  say "\`grain ci $*\` exited $status:"
  if [ -s "$out/ci.err" ]; then
    sed 's/^/release-train-task:   /' "$out/ci.err"
  else
    say "  (nothing on stderr)"
  fi
  if grep -qiE 'forbidden|does not hold|capabilit' "$out/ci.err"; then
    say "this task cannot read $repo's CI: it needs ci:read on $repo (api:ci-read@$repo)."
    say "grain-ci alone is stripped from a task filed for a repository grain CI does not gate"
    say "(ci_mode: github); check the capabilities of .grain/templates/release-train.yml and of"
    say "the task this run is for."
  fi
  fail "$what"
}

url="$(git -C "$checkout" remote get-url origin)" || fail "no origin remote in $checkout"
rm -rf "$dir"
git clone -q "$url" "$dir" || fail "could not clone $url"
cd "$dir" || fail "no $dir"
git fetch -q --tags --force origin || fail "could not fetch origin's tags"

# The rc the train will judge: the newest vX.Y.Z-rc.N with neither a
# -discarded tag nor its vX.Y.Z (or vX.Y.Z-pending) beside it. If this
# names a different rc from the train's own choice, the train reads no
# verdict for its own and says PENDING, which is never a release.
rc=""
while read -r t; do
  [ -n "$t" ] || continue
  v="${t%-rc.*}"
  git rev-parse -q --verify "refs/tags/$t-discarded" >/dev/null && continue
  git rev-parse -q --verify "refs/tags/$v" >/dev/null && continue
  git rev-parse -q --verify "refs/tags/$v-pending" >/dev/null && continue
  rc="$t"
done < <(git tag -l 'v*-rc.*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' | sort -V)

# The newest dropped rc of an unreleased version, whose grain/rc-hold holds
# the next rc when nothing is currently qualifying.
dropped=""
while read -r t; do
  [ -n "$t" ] || continue
  v="${t%-rc.*}"
  git rev-parse -q --verify "refs/tags/$v" >/dev/null && continue
  git rev-parse -q --verify "refs/tags/$v-pending" >/dev/null && continue
  dropped="$t"
done < <(git tag -l 'v*-rc.*-discarded' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+-discarded$' | sed 's/-discarded$//' | sort -V)

# Stage 0 (candidate-gate.sh select-lkg) reads only the required workflows on
# main, where every rc is cut (playbook section 5; there is no convergence
# branch since 2026-11-09), so query each required workflow directly rather
# than letting unrelated workflows on main crowd them out of one page.
cut_branch=main
required="${CANDIDATE_GATE_REQUIRED-tests release}"
if [ -n "$required" ]; then
  : > "$out/runs.jsonl"
  for wf in $required; do
    read_ci "$out/runs-$wf.json" "grain ci list could not read $wf.yml's GitHub Actions runs on $cut_branch" \
      list --repo "$repo" --ref "$cut_branch" --workflow "$wf.yml" --provider github-actions --format github --limit 500
    cat "$out/runs-$wf.json" >> "$out/runs.jsonl"
    rm -f "$out/runs-$wf.json"
  done
  jq -s '{
    total_count: (map(.total_count // (.workflow_runs | length)) | add // 0),
    workflow_runs: (map(.workflow_runs // []) | add // []),
    grain_note: ([.[].grain_note // empty | select(length > 0)] | join("; "))
  } | if .grain_note == "" then del(.grain_note) else . end' "$out/runs.jsonl" > "$out/runs.json" ||
    fail "could not combine $cut_branch's GitHub Actions runs"
else
  read_ci "$out/runs.json" "grain ci list could not read $cut_branch's GitHub Actions runs" \
    list --repo "$repo" --ref "$cut_branch" --provider github-actions --format github --limit 500
fi

app=""
env_args=()
if [ -n "$rc" ]; then
  rc_sha="$(git rev-parse "$rc^{commit}")"
  say "$rc (${rc_sha:0:12}) is in qualification; reading its verdicts"
  read_ci "$out/qual-runs.json" "grain ci list could not read nightly.yml's jobs on $rc" \
    list --repo "$repo" --ref "$rc" --workflow nightly.yml --format github-jobs
  read_ci "$out/qual-checks.json" "grain ci checks could not read grain/release-qualification on $rc" \
    checks "$rc_sha" --repo "$repo" --name grain/release-qualification --format github
  app="$(jq -r '.grain_app // empty' "$out/qual-checks.json")"
  env_args+=(RELEASE_QUALIFICATION_RUNS_JSON="$out/qual-runs.json" RELEASE_QUALIFICATION_CHECKS_JSON="$out/qual-checks.json")
fi
# If $rc fails qualification in Phase 1 of this same tick, Phase 2 checks
# grain/rc-hold on $rc's commit rather than an earlier dropped rc's; when
# nothing is currently qualifying, Phase 2 checks $dropped's commit.
hold_rc="${rc:-$dropped}"
if [ -n "$hold_rc" ]; then
  hold_sha="$(git rev-parse "$hold_rc^{commit}")"
  read_ci "$out/hold-checks.json" "grain ci checks could not read grain/rc-hold on $hold_rc" \
    checks "$hold_sha" --repo "$repo" --name grain/rc-hold --format github
  app="${app:-$(jq -r '.grain_app // empty' "$out/hold-checks.json")}"
  env_args+=(RELEASE_HOLD_CHECKS_JSON="$out/hold-checks.json")
fi
# A forge connection that is no GitHub App (a PAT) reports no grain_app
# (as does one grain cannot tell). Then the environment, or else
# scripts/release.conf's RELEASE_QUALIFICATION_APP, decides: "none" there
# qualifies on Stage 1 alone with nothing holding, and unset fails closed.
app="${app:-${RELEASE_QUALIFICATION_APP:-}}"
for f in "$out"/*.json; do
  note="$(jq -r '.grain_note // empty' "$f" 2>/dev/null || true)"
  [ -z "$note" ] || say "grain_note in $(basename "$f"): $note"
done

status=0
env RELEASE_PUSH_GRANT=train RELEASE_CI_SOURCE=grain GITHUB_TOKEN= GH_TOKEN= \
  RELEASE_RUNS_JSON="$out/runs.json" RELEASE_QUALIFICATION_APP="$app" \
  RELEASE_IMAGES=off RELEASE_NOTES_TASK=off RELEASE_NOTIFY=off \
  RELEASE_TRAIN_OUTPUT="$out/train.out" ${env_args[@]+"${env_args[@]}"} \
  bash scripts/release-from-trunk.sh || status=$?
[ "$status" = 0 ] || fail "scripts/release-from-trunk.sh exited $status"
if grep -qE '^(rc_tag|discarded_rc|promoted_version|pending_version|convergence_branch)=' "$out/train.out" 2>/dev/null; then
  echo "RELEASE_TRAIN_RESULT=acted"
else
  echo "RELEASE_TRAIN_RESULT=noop"
fi
