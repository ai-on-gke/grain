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

# Walkthrough harness for docs/test-plans/2026-11-11.md section 7 item 1:
# a simulated day of the continuous release loop (RELEASE_MAX_PER_DAY=0).
# Drives scripts/release-train-task.sh ticks under faketime over a scratch
# bare remote under /tmp and a stub `grain` CLI standing in for the Actions
# API (adapted from 2026-11-10-release-train/daycap-ticks.sh). It never
# contacts GitHub and never runs in the task checkout.
#
#   bash docs/test-runs/2026-11-11-release-supply-chain/loop-day.sh [repo-root]
#
# Prints one block per tick: the fake time (UTC and Pacific), the
# RELEASE_TRAIN_RESULT, the train outputs and the remote's tags.
set -uo pipefail

root="$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/../../..}" && pwd)"
scripts_src="$root/scripts"
task_script="$scripts_src/release-train-task.sh"
command -v faketime >/dev/null || { echo "faketime is not installed" >&2; exit 2; }
work="$(mktemp -d /tmp/loopday.XXXXXX)"
trap 'rm -rf "$work"' EXIT

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
    "pkg/cli stress under load", "upgrade from previous versions", "release image vulnerabilities", "real KVM sandboxes", "twenty two-VM boots", "real kubernetes"] | to_entries | map({id:(.key+1), run_id:7, run_attempt:1,
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
  printf '%s\n' "$out" > "$work/last.out"
}
approve() { # approve <label> <unix> <version>: a person's approve, as promote-release.yml runs it
  local label="$1" t="$2" status=0 out
  rm -rf "$work/approve" && git clone -q "$work/remote.git" "$work/approve" && git -C "$work/approve" fetch -q --tags
  out="$(cd "$work/approve" && env GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=operator@example.com \
    RELEASE_IMAGES=off RELEASE_NOTES_TASK=off RELEASE_NOTIFY=off GITHUB_TOKEN= GH_TOKEN= RELEASE_TRAIN_OUTPUT="$work/approve.out" \
    faketime -f "@$(date -u -d "@$t" '+%Y-%m-%d %H:%M:%S') x0" bash scripts/release-from-trunk.sh approve "$3" 2>&1)" || status=$?
  echo "=== approve: $label"
  echo "    fake time: $(iso "$t")  /  $(TZ=America/Los_Angeles date -d "@$t" '+%F %T %Z')  (exit $status)"
  printf '%s\n' "$out" | grep -E 'approve:|bypass|die|nothing was done' | sed 's/^/    /'
  sed 's/^/    output: /' "$work/approve.out" 2>/dev/null
  echo "    $3 tag message: $(git -C "$work/remote.git" for-each-ref --format='%(contents)' "refs/tags/$3" | tr '\n' ' ')"
  echo "    tags: $(tags)"
}
drop_reason() { git -C "$work/remote.git" for-each-ref --format='%(contents)' "refs/tags/$1-discarded" | grep '^Discard-Reason' || echo "(no Discard-Reason: red drop)"; }

# ---- One Pacific day, Wednesday 2026-11-11 (PST, UTC-8), no daily cap ----
c1="$(commit "$(utc '2026-11-11 14:00 UTC')" "feat: one")"
green_main "$(utc '2026-11-11 14:30 UTC')" "$c1"
tick "06:40 PST: cut rc.1 on c1" "$(utc '2026-11-11 14:40 UTC')"
tick "07:00 PST: rc.1 qualifying, no Stage 1 yet -> wait" "$(utc '2026-11-11 15:00 UTC')"
c2="$(commit "$(utc '2026-11-11 15:10 UTC')" "fix: two")"
green_main "$(utc '2026-11-11 15:30 UTC')" "$c1" "$c2"
stage1 "$(utc '2026-11-11 16:00 UTC')" "$c1" success
tick "08:00 PST: rc.1 green -> release v2.2.0 and cut next rc in the same tick" "$(utc '2026-11-11 16:00 UTC')"
grep -E '^[a-z_]+=' "$work/last.out" | sed 's/^/    out: /'
c3="$(commit "$(utc '2026-11-11 16:30 UTC')" "fix: three")"
green_main "$(utc '2026-11-11 16:45 UTC')" "$c1" "$c2" "$c3"
cur="$(git -C "$work/remote.git" tag -l 'v2.3.0-rc.*' | sort -V | tail -1)"
stage1 "$(utc '2026-11-11 17:00 UTC')" "$(git -C "$work/remote.git" rev-parse "$cur^{commit}")" failure
tick "09:00 PST: $cur red -> drop and cut rc.N+1 from newer c3" "$(utc '2026-11-11 17:00 UTC')"
echo "    $cur: $(drop_reason "$cur")"
cur="$(git -C "$work/remote.git" tag -l 'v2.3.0-rc.*' | grep -v discarded | sort -V | tail -1)"
echo "    newest rc: $cur on $(git -C "$work/remote.git" rev-parse --short "$cur^{commit}") (c3=$(git -C "$work/seed" rev-parse --short "$c3"))"
stage1 "$(utc '2026-11-11 19:00 UTC')" "$(git -C "$work/remote.git" rev-parse "$cur^{commit}")" success
tick "11:00 PST: $cur green -> second release of the day; no new commit" "$(utc '2026-11-11 19:00 UTC')"
tick "11:20 PST: no new commit since the release -> wait" "$(utc '2026-11-11 19:20 UTC')"
tick "11:40 PST: still nothing new -> wait" "$(utc '2026-11-11 19:40 UTC')"
c4="$(commit "$(utc '2026-11-11 20:00 UTC')" "feat: four")"
green_main "$(utc '2026-11-11 20:30 UTC')" "$c1" "$c2" "$c3" "$c4"
tick "12:40 PST: newer green commit -> cut" "$(utc '2026-11-11 20:40 UTC')"
cur="$(git -C "$work/remote.git" tag -l 'v*-rc.*' | grep -v discarded | sort -V | tail -1)"
stage1 "$(utc '2026-11-11 23:00 UTC')" "$(git -C "$work/remote.git" rev-parse "$cur^{commit}")" success
tick "15:00 PST: $cur green -> third release the same Pacific day" "$(utc '2026-11-11 23:00 UTC')"
echo "=== vX.Y.Z tags on the remote (v2.1.0 is the seed): $(git -C "$work/remote.git" for-each-ref --format='%(refname:short)' 'refs/tags/v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | paste -sd' ' -)"
echo "=== latest -> $(git -C "$work/remote.git" describe --tags --exact-match "latest^{commit}" 2>/dev/null | head -1) ($(git -C "$work/remote.git" rev-parse --short "latest^{commit}"))"
