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

# Unit tests for scripts/release-train-task.sh: drives the continuous task
# train wrapper end-to-end against a bare remote and a stub `grain` CLI that
# serves GitHub-shaped workflow runs, nightly.yml jobs, and check runs.
# tests/deploy/release_trunk_scripts_test.go runs this in CI; by hand:
#
#   bash scripts/release-train-task_test.sh
set -euo pipefail

scripts_src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
task_script="$scripts_src/release-train-task.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

now=1794000000
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

# Stub `grain` CLI: logs every invocation in $FAKE_CALLS and answers `ci list`
# and `ci checks` from fixture files in $FAKE_DIR.
mkdir -p "$work/bin" "$work/fixtures"
cat > "$work/bin/grain" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$FAKE_CALLS"
if [ -n "${FAKE_CI_FORBIDDEN:-}" ] && [ "${1:-}" = ci ]; then
  echo 'grain: forbidden: grain ci: that tool belongs to the "https://grain.dev/capabilities/grain#ci" capability, and this run does not hold it' >&2
  exit 3
fi
sub="${1:-}"; shift || true
sub2="${1:-}"; shift || true
case "$sub $sub2" in
  "ci list")
    wf="" fmt="" ref=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --workflow) wf="$2"; shift 2 ;;
        --format) fmt="$2"; shift 2 ;;
        --ref) ref="$2"; shift 2 ;;
        --repo|--provider|--limit) shift 2 ;;
        *) shift ;;
      esac
    done
    if [ "$fmt" = "github-jobs" ]; then
      cat "$FAKE_DIR/qual-runs.json"
    elif [ -n "$wf" ] && [ -f "$FAKE_DIR/runs-${ref//\//_}-$wf.json" ]; then
      cat "$FAKE_DIR/runs-${ref//\//_}-$wf.json"
    elif [ -n "$wf" ] && [ -f "$FAKE_DIR/runs-$wf.json" ]; then
      cat "$FAKE_DIR/runs-$wf.json"
    else
      echo '{"total_count":0,"workflow_runs":[]}'
    fi
    ;;
  "ci checks")
    sha="$1"; shift
    name=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --name) name="$2"; shift 2 ;;
        --repo|--format) shift 2 ;;
        *) shift ;;
      esac
    done
    key="${name//\//_}"
    if [ -f "$FAKE_DIR/checks-$sha-$key.json" ]; then
      cat "$FAKE_DIR/checks-$sha-$key.json"
    elif [ -f "$FAKE_DIR/checks-$key.json" ]; then
      cat "$FAKE_DIR/checks-$key.json"
    else
      printf '{"total_count":0,"check_runs":[],"grain_app":%s}\n' "${FAKE_APP_JSON:-\"grain-forge\"}"
    fi
    ;;
  "api GET")
    if [ -n "${FAKE_RELEASE_API_GARBLED:-}" ]; then
      echo '<html>bad gateway</html>'
    elif [ -n "${FAKE_RELEASE_API_FAIL:-}" ]; then
      echo "grain: forbidden: GET /api/repos/ai-on-gke/grain/release" >&2
      exit 3
    elif [ -f "$FAKE_DIR/release.json" ]; then
      cat "$FAKE_DIR/release.json"
    else
      echo '{"repo":"ai-on-gke/grain","enabled":true}'
    fi
    ;;
  *)
    echo "unexpected grain call: $sub $sub2 $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$work/bin/grain"

setup_repo() {
  rm -rf "$work/remote.git" "$work/seed" "$work/checkout" "$work/train" "$work/fixtures"
  mkdir -p "$work/fixtures"
  git init -q --bare -b main "$work/remote.git"
  git clone -q "$work/remote.git" "$work/seed"
  (
    cd "$work/seed"
    git checkout -q -b main
    mkdir -p scripts
    cp "$scripts_src/release-from-trunk.sh" "$scripts_src/candidate-gate.sh" "$scripts_src/release-notes.sh" scripts/
    cat > scripts/release.conf <<'CONF'
RELEASE_MAJOR=2
RELEASE_FLOOR_MINOR=0
RELEASE_MIN_INTERVAL_MINUTES=0
RELEASE_AUTO_PROMOTE=true
RELEASE_MAX_RC_ATTEMPTS=3
RELEASE_NEVER_PROMOTE=""
CONF
    git add scripts
    GIT_COMMITTER_DATE="@$((now - 90000)) +0000" git commit -q -m "chore: seed release scripts"
    # Released v2.1.0, preceded by an old v2.1.0-rc.1-discarded that must be
    # ignored once v2.1.0 is released.
    GIT_COMMITTER_DATE="@$((now - 86400)) +0000" git tag -a v2.1.0-rc.1 -m "rc1"
    GIT_COMMITTER_DATE="@$((now - 85000)) +0000" git tag -a v2.1.0-rc.1-discarded -m "dropped"
    GIT_COMMITTER_DATE="@$((now - 80000)) +0000" git tag -a v2.1.0-rc.2 -m "rc2"
    GIT_COMMITTER_DATE="@$((now - 72000)) +0000" git tag -a v2.1.0 -m "Release v2.1.0"
    git tag -fa latest "v2.1.0^{commit}" -m "latest"
    echo one > feature.txt
    git add feature.txt
    GIT_COMMITTER_DATE="@$((now - 7200)) +0000" git commit -q -m "feat: add first feature"
    echo two >> feature.txt
    git add feature.txt
    GIT_COMMITTER_DATE="@$((now - 3600)) +0000" git commit -q -m "fix: repair edge case"
    git push -q origin main --tags
  )
  mapfile -t commits < <(git -C "$work/seed" rev-list --reverse main)
  c0="${commits[0]}" c1="${commits[1]}" c2="${commits[2]}"
  git clone -q "$work/remote.git" "$work/checkout"
  echo '{"total_count":0,"jobs":[]}' > "$work/fixtures/qual-runs.json"
}

green_workflow() { # green_workflow <branch> <wf.yml> <sha>...
  local branch="$1" wf="$2" sha id=100 items=()
  shift 2
  for sha in "$@"; do
    id=$((id + 1))
    items+=("$(jq -cn --argjson id "$id" --arg wf ".github/workflows/$wf" --arg br "$branch" --arg sha "$sha" --arg t "$(iso $((now - 1800)))" \
      '{id:$id, path:$wf, head_branch:$br, head_sha:$sha, event:"push", status:"completed", conclusion:"success", run_started_at:$t, updated_at:$t, repository:{full_name:"ai-on-gke/grain"}, head_repository:{full_name:"ai-on-gke/grain"}}')")
  done
  printf '%s\n' "${items[@]}" | jq -s '{total_count: length, workflow_runs: .}' > "$work/fixtures/runs-${branch//\//_}-$wf.json"
}

stage1_jobs() { # stage1_jobs <sha> <govulncheck-attempt1> [<govulncheck-attempt2>]
  local sha="$1" a1="$2" a2="${3:-}" first=1
  {
    printf '{"total_count":18,"jobs":['
    for name in "go, shuffled and repeated (1)" "state repository soak" "sustained load" "metadata and model scalability" \
      "merge liveness stress" "end-to-end long term tests" "long randomized cluster simulation" "postgres concurrency and stress" \
      "reverse tunnel multiplexer stress" "ui e2e, repeated (1)" "installer trixie and hostmode" "govulncheck" \
      "release image vulnerabilities" "pkg/cli stress under load" "upgrade from previous versions" \
      "real KVM sandboxes" "twenty two-VM boots" "real kubernetes"; do
      [ $first = 1 ] || printf ','
      first=0
      printf '{"id":%s,"run_id":7,"run_attempt":1,"workflow_name":"nightly","head_sha":"%s","name":"%s","status":"completed","conclusion":"%s","created_at":"%s","started_at":null,"completed_at":null}' \
        "${#name}" "$sha" "$name" "$([ "$name" = govulncheck ] && echo "$a1" || echo success)" "$(iso $((now - 3600)))"
    done
    if [ -n "$a2" ]; then
      printf ',{"id":99,"run_id":7,"run_attempt":2,"workflow_name":"nightly","head_sha":"%s","name":"govulncheck","status":"completed","conclusion":"%s","created_at":"%s","started_at":null,"completed_at":null}' \
        "$sha" "$a2" "$(iso $((now - 1800)))"
    fi
    printf ']}'
  } > "$work/fixtures/qual-runs.json"
}

remote_tag() {
  git -C "$work/remote.git" rev-parse -q --verify "refs/tags/$1^{commit}" 2>/dev/null || true
}

failures=0
cases=0
out=""
status=0
calls=""

run_tick() {
  : > "$work/calls"
  status=0
  out="$(env GRAIN="$work/bin/grain" FAKE_CALLS="$work/calls" FAKE_DIR="$work/fixtures" \
    RELEASE_TASK_CHECKOUT="$work/checkout" RELEASE_TASK_DIR="$work/train" \
    RELEASE_TASK_REPO=ai-on-gke/grain RELEASE_NOW="$now" "$@" \
    bash "$task_script" 2>&1)" || status=$?
  calls="$(cat "$work/calls")"
}

expect_eq() {
  local name="$1" got="$2" want="$3"
  cases=$((cases + 1))
  if [ "$got" != "$want" ]; then
    echo "FAIL $name: got '$got', want '$want'"
    printf '%s\n--- calls ---\n%s\n' "$out" "$calls" | sed 's/^/    /'
    failures=$((failures + 1))
  fi
}

expect_has() {
  local name="$1" hay="$2" needle="$3"
  cases=$((cases + 1))
  if [[ "$hay" != *"$needle"* ]]; then
    echo "FAIL $name: missing '$needle'"
    printf '%s\n--- calls ---\n%s\n' "$out" "$calls" | sed 's/^/    /'
    failures=$((failures + 1))
  fi
}

expect_not_has() {
  local name="$1" hay="$2" needle="$3"
  cases=$((cases + 1))
  if [[ "$hay" == *"$needle"* ]]; then
    echo "FAIL $name: unexpectedly matched '$needle'"
    printf '%s\n--- calls ---\n%s\n' "$out" "$calls" | sed 's/^/    /'
    failures=$((failures + 1))
  fi
}

# 1. Cuts v2.2.0-rc.1 from per-workflow main queries, ignoring already-released
#    v2.1.0-rc.1-discarded.
setup_repo
green_workflow main tests.yml "$c1"
green_workflow main release.yml "$c1"
run_tick
expect_eq "cut rc.1: exit" "$status" 0
expect_has "cut rc.1: acted" "$out" "RELEASE_TRAIN_RESULT=acted"
expect_eq "cut rc.1: tag commit" "$(remote_tag v2.2.0-rc.1)" "$c1"
expect_has "cut rc.1: queries tests.yml" "$calls" "--ref main --workflow tests.yml"
expect_has "cut rc.1: queries release.yml" "$calls" "--ref main --workflow release.yml"
expect_not_has "cut rc.1: skips released v2.1.0-rc.1-discarded" "$calls" "$c0"

# 2. Same-tick Phase 1 failure on v2.2.0-rc.1 reads grain/rc-hold on v2.2.0-rc.1's
#    commit ($c1) and holds v2.2.0-rc.2 until the hold completes.
green_workflow main tests.yml "$c1" "$c2"
green_workflow main release.yml "$c1" "$c2"
stage1_jobs "$c1" failure failure
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 600)))" \
  '{total_count:1, check_runs:[{id:11, name:"grain/rc-hold", head_sha:$sha, status:"in_progress", conclusion:null, started_at:$t, output:{summary:"1 open rc-blocker"}, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c1-grain_rc-hold.json"
run_tick
expect_eq "same-tick drop + hold: exit" "$status" 0
expect_eq "same-tick drop + hold: discards rc.1" "$(remote_tag v2.2.0-rc.1-discarded)" "$c1"
expect_eq "same-tick drop + hold: holds rc.2" "$(remote_tag v2.2.0-rc.2)" ""
expect_has "same-tick drop + hold: reports hold" "$out" "v2.2.0-rc.2 is held by grain/rc-hold"
expect_has "same-tick drop + hold: acted on discard" "$out" "RELEASE_TRAIN_RESULT=acted"
# A tick that stops at the rc-hold never reads main's runs (grain/task-4393).
expect_not_has "same-tick drop + hold: reads no main runs" "$calls" "--ref main --workflow"

# 3. An empty grain_app (a forge connection that is no GitHub App, such as a
#    PAT, or one grain cannot tell) with no RELEASE_QUALIFICATION_APP set
#    anywhere fails closed: it is never "none" by default.
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 600)))" \
  '{total_count:1, check_runs:[{id:11, name:"grain/rc-hold", head_sha:$sha, status:"in_progress", conclusion:null, started_at:$t, output:{summary:"1 open rc-blocker"}, app:{slug:"grain-forge"}}], grain_app:""}' \
  > "$work/fixtures/checks-$c1-grain_rc-hold.json"
run_tick FAKE_APP_JSON='""' RELEASE_QUALIFICATION_APP=
expect_eq "missing grain_app: exit 1" "$status" 1
expect_has "missing grain_app: result failed" "$out" "RELEASE_TRAIN_RESULT=failed"
expect_has "missing grain_app: explains RELEASE_QUALIFICATION_APP" "$out" "RELEASE_QUALIFICATION_APP is unset"
expect_not_has "missing grain_app: is never none by default" "$out" "RELEASE_QUALIFICATION_APP=none"
expect_eq "missing grain_app: still holds rc.2" "$(remote_tag v2.2.0-rc.2)" ""

#    With RELEASE_QUALIFICATION_APP=none set explicitly (as scripts/release.conf
#    may), no app is trusted, so no grain/rc-hold holds rc.2 (dry run, so the
#    next case still sees the hold).
run_tick FAKE_APP_JSON='""' RELEASE_QUALIFICATION_APP=none DRY_RUN=true
expect_eq "empty grain_app: exit 0" "$status" 0
expect_not_has "empty grain_app: does not fail" "$out" "RELEASE_TRAIN_RESULT=failed"
expect_has "empty grain_app: trusts no rc-hold" "$out" "RELEASE_QUALIFICATION_APP=none, so no grain/rc-hold check run"
expect_not_has "empty grain_app: is not held" "$out" "is held by grain/rc-hold"
expect_eq "empty grain_app: dry run pushes no rc.2" "$(remote_tag v2.2.0-rc.2)" ""

# 4. Once grain/rc-hold completes on $c1, the next tick cuts v2.2.0-rc.2 on $c2.
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 300)))" \
  '{total_count:1, check_runs:[{id:11, name:"grain/rc-hold", head_sha:$sha, status:"completed", conclusion:"success", started_at:$t, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c1-grain_rc-hold.json"
run_tick
expect_eq "hold cleared: exit" "$status" 0
expect_eq "hold cleared: cuts rc.2 on c2" "$(remote_tag v2.2.0-rc.2)" "$c2"

# 5. Once rc.2 is dropped, rc.3 is cut from main's newest green commit, like
#    every rc (no release/v2.2.0 convergence branch since 2026-11-09).
(
  cd "$work/seed"
  git fetch -q origin --tags
  GIT_COMMITTER_DATE="@$((now - 200)) +0000" git tag -a v2.2.0-rc.2-discarded "$c2" -m "rc2 dropped"
  git checkout -q main
  git reset -q --hard "$c2"
  echo three >> feature.txt
  git add feature.txt
  GIT_COMMITTER_DATE="@$((now - 100)) +0000" git commit -q -m "fix: the regression rc.2 hit"
  git push -q origin main --tags
)
c3="$(git -C "$work/seed" rev-parse main)"
green_workflow main tests.yml "$c1" "$c2" "$c3"
green_workflow main release.yml "$c1" "$c2" "$c3"
jq -cn --arg sha "$c2" --arg t "$(iso $((now - 90)))" \
  '{total_count:1, check_runs:[{id:12, name:"grain/rc-hold", head_sha:$sha, status:"completed", conclusion:"success", started_at:$t, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c2-grain_rc-hold.json"
run_tick
expect_eq "rc.3 from main: exit" "$status" 0
expect_has "rc.3 from main: queries main" "$calls" "--ref main --workflow release.yml"
expect_not_has "rc.3 from main: no release branch" "$calls" "--ref release/v2.2.0"
expect_eq "rc.3 from main: cuts rc.3 on c3" "$(remote_tag v2.2.0-rc.3)" "$c3"

# 6. While rc.3 is still qualifying (Stage 1 has no verdict yet), the tick is
#    a noop that reads the rc's verdicts and never main's up to 500 runs of
#    each required workflow -- most of what a tick used to read
#    (grain/task-4393).
echo '{"total_count":0,"jobs":[]}' > "$work/fixtures/qual-runs.json"
run_tick
expect_eq "rc qualifying: exit" "$status" 0
expect_has "rc qualifying: noop" "$out" "RELEASE_TRAIN_RESULT=noop"
expect_has "rc qualifying: reads the rc's jobs" "$calls" "--ref v2.2.0-rc.3 --workflow nightly.yml"
expect_not_has "rc qualifying: reads no main runs" "$calls" "--ref main --workflow"

# 7. A task that cannot read CI (grain-ci stripped, no api:ci-read) fails with
#    grain's own refusal, the command that hit it and the grant it needs.
run_tick FAKE_CI_FORBIDDEN=1
expect_eq "ci forbidden: exit 1" "$status" 1
expect_has "ci forbidden: result failed" "$out" "RELEASE_TRAIN_RESULT=failed"
expect_has "ci forbidden: names the command and status" "$out" "\`grain ci list --repo ai-on-gke/grain"
expect_has "ci forbidden: exit status" "$out" "exited 3:"
expect_has "ci forbidden: quotes grain's stderr" "$out" "release-train-task:   grain: forbidden: grain ci:"
expect_has "ci forbidden: names the missing grant" "$out" "api:ci-read@ai-on-gke/grain"

# 8. The same refusal when it is main's runs that cannot be read, which
#    release-from-trunk.sh reads only once it looks for a commit to cut.
setup_repo
run_tick FAKE_CI_FORBIDDEN=1
expect_eq "main runs forbidden: exit 1" "$status" 1
expect_has "main runs forbidden: result failed" "$out" "RELEASE_TRAIN_RESULT=failed"
expect_has "main runs forbidden: names the command" "$out" "\`grain ci list --repo ai-on-gke/grain --ref main --workflow tests.yml"
expect_has "main runs forbidden: names the missing grant" "$out" "api:ci-read@ai-on-gke/grain"
expect_eq "main runs forbidden: one result line" "$(grep -c '^RELEASE_TRAIN_RESULT=' <<<"$out")" 1

# 9. Held tick promotes a green rc and does not cut (detected via
#    `grain api GET /api/repos/<repo>/release?branch=main`).
setup_repo
(
  cd "$work/seed"
  GIT_COMMITTER_DATE="@$((now - 1800)) +0000" git tag -a v2.2.0-rc.1 "$c1" -m "rc1"
  git push -q origin --tags
)
green_workflow main tests.yml "$c1" "$c2"
green_workflow main release.yml "$c1" "$c2"
stage1_jobs "$c1" success
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 600)))" \
  '{total_count:1, check_runs:[{id:20, name:"grain/release-qualification", head_sha:$sha, status:"completed", conclusion:"success", started_at:$t, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c1-grain_release-qualification.json"
cat > "$work/fixtures/release.json" <<'JSON'
{"repo":"ai-on-gke/grain","enabled":true,"baseBranchPublishing":{"repo":"ai-on-gke/grain","branch":"main","failures":3,"triageTaskID":"4256","gated":true}}
JSON
run_tick RELEASE_MAX_PER_DAY=0
expect_eq "held green rc: exit 0" "$status" 0
expect_has "held green rc: acted" "$out" "RELEASE_TRAIN_RESULT=acted"
expect_eq "held green rc: promotes v2.2.0" "$(remote_tag v2.2.0)" "$c1"
expect_eq "held green rc: moves latest" "$(remote_tag latest)" "$c1"
expect_eq "held green rc: does not cut v2.3.0-rc.1" "$(remote_tag v2.3.0-rc.1)" ""
expect_has "held green rc: outputs held=publishing (cut skipped)" "$out" "held=publishing (cut skipped)"
expect_has "held green rc: names holding triage task" "$out" "triage task 4256"
expect_has "held green rc: probes release API" "$calls" "api GET /api/repos/ai-on-gke/grain/release?branch=main"
expect_not_has "held green rc: skips main runs" "$calls" "--ref main --workflow"
expect_not_has "held green rc: skips rc-hold lookup" "$calls" "grain/rc-hold"

# 10. Held tick with nothing in qualification is a noop that reports
#     `held=publishing (cut skipped)` and cuts nothing.
run_tick RELEASE_MAX_PER_DAY=0
expect_eq "held noop: exit 0" "$status" 0
expect_has "held noop: noop" "$out" "RELEASE_TRAIN_RESULT=noop"
expect_eq "held noop: cuts no rc" "$(remote_tag v2.3.0-rc.1)" ""
expect_has "held noop: outputs held=publishing (cut skipped)" "$out" "held=publishing (cut skipped)"
expect_not_has "held noop: skips main runs" "$calls" "--ref main --workflow"

# 11. Held tick with a red rc drops it and does not cut rc.2.
setup_repo
(
  cd "$work/seed"
  GIT_COMMITTER_DATE="@$((now - 1800)) +0000" git tag -a v2.2.0-rc.1 "$c1" -m "rc1"
  git push -q origin --tags
)
green_workflow main tests.yml "$c1" "$c2"
green_workflow main release.yml "$c1" "$c2"
stage1_jobs "$c1" failure failure
cat > "$work/fixtures/release.json" <<'JSON'
{"repo":"ai-on-gke/grain","enabled":true,"baseBranchPublishing":{"repo":"ai-on-gke/grain","branch":"main","failures":3,"triageTaskID":"4256","gated":true}}
JSON
run_tick RELEASE_MAX_PER_DAY=0 RELEASE_QUALIFICATION_APP=none
expect_eq "held red rc: exit 0" "$status" 0
expect_has "held red rc: acted" "$out" "RELEASE_TRAIN_RESULT=acted"
expect_eq "held red rc: drops v2.2.0-rc.1" "$(remote_tag v2.2.0-rc.1-discarded)" "$c1"
expect_eq "held red rc: does not cut v2.2.0-rc.2" "$(remote_tag v2.2.0-rc.2)" ""
expect_has "held red rc: outputs held=publishing (cut skipped)" "$out" "held=publishing (cut skipped)"
expect_not_has "held red rc: skips main runs" "$calls" "--ref main --workflow"
expect_not_has "held red rc: skips rc-hold lookup" "$calls" "grain/rc-hold"

# 12. Unheld tick is unchanged: promotes green rc and cuts the next rc in the
#     same tick (when RELEASE_MAX_PER_DAY=0).
setup_repo
(
  cd "$work/seed"
  GIT_COMMITTER_DATE="@$((now - 1800)) +0000" git tag -a v2.2.0-rc.1 "$c1" -m "rc1"
  git push -q origin --tags
)
green_workflow main tests.yml "$c1" "$c2"
green_workflow main release.yml "$c1" "$c2"
stage1_jobs "$c1" success
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 600)))" \
  '{total_count:1, check_runs:[{id:21, name:"grain/release-qualification", head_sha:$sha, status:"completed", conclusion:"success", started_at:$t, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c1-grain_release-qualification.json"
echo '{"repo":"ai-on-gke/grain","enabled":true}' > "$work/fixtures/release.json"
run_tick RELEASE_MAX_PER_DAY=0
expect_eq "unheld green rc: exit 0" "$status" 0
expect_has "unheld green rc: acted" "$out" "RELEASE_TRAIN_RESULT=acted"
expect_eq "unheld green rc: promotes v2.2.0" "$(remote_tag v2.2.0)" "$c1"
expect_eq "unheld green rc: cuts v2.3.0-rc.1 on c2" "$(remote_tag v2.3.0-rc.1)" "$c2"
expect_not_has "unheld green rc: not held" "$out" "held=publishing"

# 13. A tick that cannot read the publishing hold fails closed on cut: it
#     still promotes the green rc in qualification, but cuts nothing, since a
#     hold it could not see would otherwise let a tick cut while main's
#     publishing is held.
setup_repo
(
  cd "$work/seed"
  GIT_COMMITTER_DATE="@$((now - 1800)) +0000" git tag -a v2.2.0-rc.1 "$c1" -m "rc1"
  git push -q origin --tags
)
green_workflow main tests.yml "$c1" "$c2"
green_workflow main release.yml "$c1" "$c2"
stage1_jobs "$c1" success
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 600)))" \
  '{total_count:1, check_runs:[{id:22, name:"grain/release-qualification", head_sha:$sha, status:"completed", conclusion:"success", started_at:$t, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c1-grain_release-qualification.json"
run_tick RELEASE_MAX_PER_DAY=0 FAKE_RELEASE_API_FAIL=1
expect_eq "hold probe fails: exit 0" "$status" 0
expect_has "hold probe fails: acted" "$out" "RELEASE_TRAIN_RESULT=acted"
expect_eq "hold probe fails: promotes v2.2.0" "$(remote_tag v2.2.0)" "$c1"
expect_eq "hold probe fails: does not cut v2.3.0-rc.1" "$(remote_tag v2.3.0-rc.1)" ""
expect_has "hold probe fails: says cut skipped" "$out" "held=publishing-unknown (cut skipped)"
expect_has "hold probe fails: warns" "$out" "WARNING: could not check publishing hold"

# 14. An answer that is not JSON fails closed on cut the same way, rather
#     than reading as "not held".
setup_repo
(
  cd "$work/seed"
  GIT_COMMITTER_DATE="@$((now - 1800)) +0000" git tag -a v2.2.0-rc.1 "$c1" -m "rc1"
  git push -q origin --tags
)
green_workflow main tests.yml "$c1" "$c2"
green_workflow main release.yml "$c1" "$c2"
stage1_jobs "$c1" success
jq -cn --arg sha "$c1" --arg t "$(iso $((now - 600)))" \
  '{total_count:1, check_runs:[{id:23, name:"grain/release-qualification", head_sha:$sha, status:"completed", conclusion:"success", started_at:$t, app:{slug:"grain-forge"}}], grain_app:"grain-forge"}' \
  > "$work/fixtures/checks-$c1-grain_release-qualification.json"
run_tick RELEASE_MAX_PER_DAY=0 FAKE_RELEASE_API_GARBLED=1
expect_eq "garbled hold probe: exit 0" "$status" 0
expect_eq "garbled hold probe: promotes v2.2.0" "$(remote_tag v2.2.0)" "$c1"
expect_eq "garbled hold probe: does not cut v2.3.0-rc.1" "$(remote_tag v2.3.0-rc.1)" ""
expect_has "garbled hold probe: says cut skipped" "$out" "held=publishing-unknown (cut skipped)"

echo
echo "$((cases - failures))/$cases passed"
[ "$failures" -eq 0 ]
