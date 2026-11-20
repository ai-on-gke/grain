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
#
# docs/test-plans/2026-11-18.md section 6 item 4: one release cut by the
# real train (scripts/release-train-task.sh -> release-from-trunk.sh) in a
# scratch clone against a local bare remote, with a fake `grain` for CI
# verdicts (from docs/test-runs/2026-11-10-release-train/daycap-ticks.sh).
# After each tick it prints the remote's tags and what the release audit's
# section 0 guard (.grain/templates/release-audit.yml, "the release is not
# tagged yet") decides for the version, and at the end the user.name the
# train left in its scratch clone's .git/config. Usage: train-then-audit.sh [repo root]

set -uo pipefail

root="$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/../../..}" && pwd)"
scripts_src="$root/scripts"
task_script="$scripts_src/release-train-task.sh"
command -v faketime >/dev/null || { echo "faketime is not installed" >&2; exit 2; }
work="$(mktemp -d /tmp/train-audit.XXXXXX)"
[ -n "${KEEP:-}" ] || trap 'rm -rf "$work"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

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
    # GET /api/repos/<repo>/release: main's publishing is not held.
    echo '{"baseBranchPublishing":{"gated":false}}'
    ;;
  *)
    echo "unexpected grain call: $sub $sub2 $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$work/bin/grain"


utc() { date -u -d "$1" +%s; } # utc "<date> UTC": unix seconds
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

git init -q --bare -b main "$work/remote.git"
git clone -q "$work/remote.git" "$work/seed" 2>/dev/null
cd "$work/seed" || exit 1
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
RELEASE_MAX_PER_DAY=0
RELEASE_DAY_TZ=America/Los_Angeles
RELEASE_DAY_CAP_EXEMPT=""
RELEASE_QUALIFICATION_APP=none
CONF
git add scripts
d0="$(utc '2026-10-28 12:00 UTC')"
GIT_COMMITTER_DATE="@$d0 +0000" git commit -q -m "chore: seed release scripts"
GIT_COMMITTER_DATE="@$d0 +0000" git tag -a v2.1.0 -m "Release v2.1.0"
git tag -fa latest "v2.1.0^{commit}" -m latest
git push -q origin main --tags
cd - >/dev/null || exit 1
git clone -q "$work/remote.git" "$work/checkout"
echo '{"total_count":0,"jobs":[]}' > "$work/fixtures/qual-runs.json"

commit() { # commit <unix> <msg>: a new main commit, echoed
  (cd "$work/seed" && echo "$2" >> f.txt && git add f.txt &&
    GIT_COMMITTER_DATE="@$1 +0000" GIT_AUTHOR_DATE="@$1 +0000" git commit -q -m "$2" && git push -q origin main) >&2
  git -C "$work/seed" rev-parse HEAD
}
green_main() { # green_main <unix> <sha>...: tests.yml and release.yml green on each
  local t="$1" wf sha id=100 items
  shift
  for wf in tests.yml release.yml; do
    items=()
    for sha in "$@"; do
      id=$((id + 1))
      items+=("$(jq -cn --argjson id "$id" --arg wf ".github/workflows/$wf" --arg sha "$sha" --arg t "$(iso "$t")" \
        '{id:$id, path:$wf, head_branch:"main", head_sha:$sha, event:"push", status:"completed", conclusion:"success", run_started_at:$t, updated_at:$t, repository:{full_name:"ai-on-gke/grain"}, head_repository:{full_name:"ai-on-gke/grain"}}')")
    done
    printf '%s\n' "${items[@]}" | jq -s '{total_count: length, workflow_runs: .}' > "$work/fixtures/runs-main-$wf.json"
  done
}
stage1() { # stage1 <unix> <sha> <conclusion>: every nightly.yml job on <sha>
  jq -n --arg sha "$2" --arg c "$3" --arg t "$(iso "$1")" '{total_count:18, jobs: [
    "go, shuffled and repeated (1)", "state repository soak", "sustained load", "metadata and model scalability",
    "merge liveness stress", "end-to-end long term tests", "long randomized cluster simulation", "postgres concurrency and stress",
    "reverse tunnel multiplexer stress", "ui e2e, repeated (1)", "installer trixie and hostmode", "govulncheck",
    "pkg/cli stress under load", "upgrade from previous versions",
    "release image vulnerabilities", "real KVM sandboxes", "twenty two-VM boots", "real kubernetes"] | to_entries | map({id:(.key+1), run_id:7, run_attempt:1,
    workflow_name:"nightly", head_sha:$sha, name:.value, status:"completed", conclusion:$c, created_at:$t, started_at:$t, completed_at:$t})}' \
    > "$work/fixtures/qual-runs.json"
}
tags() { git -C "$work/remote.git" tag -l 'v*' | sort -V | paste -sd' ' -; }
tick() { # tick <label> <unix> [env...]
  local label="$1" t="$2" status=0 out
  shift 2
  out="$(env GRAIN="$work/bin/grain" FAKE_CALLS="$work/calls" FAKE_DIR="$work/fixtures" FAKE_APP_JSON='""' \
    RELEASE_TASK_CHECKOUT="$work/checkout" RELEASE_TASK_DIR="$work/train" RELEASE_TASK_REPO=ai-on-gke/grain "$@" \
    faketime -f "@$(date -u -d "@$t" '+%Y-%m-%d %H:%M:%S') x0" bash "$task_script" 2>&1)" || status=$?
  echo "=== tick: $label"
  echo "    fake time: $(iso "$t")  /  $(TZ=America/Los_Angeles date -d "@$t" '+%F %T %Z')  (exit $status)"
  printf '%s\n' "$out" | grep -E '^RELEASE_TRAIN_RESULT=|release day is|dropping|day_done|phase 2: .*(nothing is cut|held|cut)|qualified|promot|staged|bypass|exempt|EXEMPT|cutting|cut v|PENDING|still|waits' | sed 's/^/    /'
  echo "    tags: $(tags)"
  printf '%s\n' "$out" > "$work/last.out"; printf "=== %s\n%s\n" "$label" "$out" >> "$work/all.out"
}
# The audit's section 0 guard, as its body says to run it.
audit_guard() { # audit_guard <version>
  local v="$1"
  if git ls-remote --exit-code -q "$work/remote.git" "refs/tags/$v" >/dev/null; then
    echo "    audit $v: tagged ($(git -C "$work/remote.git" rev-parse "$v^{commit}" | cut -c1-12)), the audit runs"
  elif [ -n "$(git ls-remote "$work/remote.git" "refs/tags/$v-rc.*" "refs/tags/$v-pending")" ]; then
    echo "    audit $v: not tagged yet but has an rc or -pending: resume_later, no check run"
  else
    echo "    audit $v: no such release: ask_question"
  fi
}

c1="$(commit "$(utc '2026-11-17 08:00 UTC')" "feat: one")"
green_main "$(utc '2026-11-17 08:30 UTC')" "$c1"
v="v2.2.0"
audit_guard "$v"
tick "cut rc.1" "$(utc '2026-11-17 09:00 UTC')"
audit_guard "$v"
stage1 "$(utc '2026-11-17 11:00 UTC')" "$c1" success
tick "rc.1 green -> released" "$(utc '2026-11-17 11:00 UTC')"
audit_guard "$v"
echo "    $v tag message: $(git -C "$work/remote.git" for-each-ref --format='%(contents)' "refs/tags/$v" | tr '\n' ' ')"
echo "    latest: $(git -C "$work/remote.git" rev-parse 'latest^{commit}' | cut -c1-12), c1: $(echo "$c1" | cut -c1-12)"
tick "the next tick: nothing new" "$(utc '2026-11-17 11:20 UTC')"
echo "=== the scratch clone's identity (never the walker's checkout)"
for d in "$work/checkout" "$work/train"/*/; do
  [ -d "$d/.git" ] || [ -f "$d/HEAD" ] || continue
  echo "    $d: user.name=$(git -C "$d" config --local user.name || echo '(unset)')"
done
