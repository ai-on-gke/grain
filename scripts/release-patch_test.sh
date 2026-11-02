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

# Unit tests for scripts/patch-tag-guard.sh and scripts/release-patch.sh,
# against a bare remote and a fake `gh` that answers for release.yml.
# tests/deploy/release_trunk_scripts_test.go runs this in CI; by hand:
#
#   bash scripts/release-patch_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

scripts="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
unset GITHUB_ACTIONS

failures=0
cases=0
out=""
status=0

# run <dir> <script> <args...>: output in $out, exit status in $status.
run() {
  local dir="$1" script="$2"
  shift 2
  status=0
  out="$(cd "$dir" && bash "$scripts/$script" "$@" 2>&1)" || status=$?
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

expect() { # expect <case> <command...>: the command succeeds
  local name="$1"
  shift
  cases=$((cases + 1))
  if "$@" >/dev/null 2>&1; then
    echo "ok   $name"
  else
    echo "FAIL $name: $* failed"
    failures=$((failures + 1))
  fi
}

remote="$work/remote.git"
git init -q --bare -b main "$remote"
git init -q -b main "$work/seed"
cd "$work/seed"
commit() { # commit <file> <subject>: prints the new commit
  echo "$2" > "$1"
  git add "$1"
  git commit -q -m "$2"
  git rev-parse HEAD
}
base=$(commit base "chore: start")
git tag -a v1.0.0 -m v1.0.0 "$base"
git tag -a v1.2.0 -m v1.2.0 "$base"
feat_a=$(commit a "feat(ui): add a shiny thing")
fix_b=$(commit b "fix(api): stop dropping requests")
chore_c=$(commit c "chore: tidy")
git tag -a v1.5.0 -m v1.5.0 "$chore_c"
fix_d=$(commit d "fix: close the file")
git merge -q --no-ff -m "Merge pull request #9 from x/y" "$(git commit-tree -p "$fix_d" -m "fix(ci): retry once" "$(git write-tree)")"
main=$(git rev-parse HEAD)
# Hand-tagged on main, the way v3.0.1 and v3.0.2 were.
git tag -a v1.2.1 -m v1.2.1 "$fix_b"
# Fixes only past v1.5.0, so tagging main is a patch after all.
git tag -a v1.5.1 -m v1.5.1 "$main"
# A release branch that is just main.
git branch release/v1.2.2 "$main"
git tag -a v1.2.2 -m v1.2.2 "$main"
git remote add origin "$remote"
git push -q origin main release/v1.2.2 --tags
cd - >/dev/null

git clone -q "$remote" "$work/clone"
clone="$work/clone"

# ---- patch-tag-guard.sh ----

run "$clone" patch-tag-guard.sh v1.0.0 refs/heads/main refs/tags/v1.2.0 ""
check "guard passes tags that are not patches" 0 "v1.0.0 is not a patch tag" "v1.2.0 is not a patch tag"

run "$clone" patch-tag-guard.sh refs/tags/v1.2.1
check "guard refuses a patch tagged on main" 1 \
  "refusing v1.2.1" "there is no release/v1.2.1 branch" \
  "v1.2.1 carries these feat: commits:" "  feat(ui): add a shiny thing"
lacks "guard lists only feat: subjects by name" "fix(api): stop dropping requests"

run "$clone" patch-tag-guard.sh v1.2.2
check "guard refuses a release branch that is just main" 1 \
  "refusing v1.2.2" "4 of the 4 commits in v1.2.1..v1.2.2 are main's own" \
  "feat(ui): add a shiny thing" "and 1 other commits that are not fix:"

run "$clone" patch-tag-guard.sh v1.5.1
check "guard passes a fixes-only patch even off main" 0 "v1.5.1" "carries only fix: commits past v1.5.0; ok"

run "$clone" patch-tag-guard.sh v1.9.1
check "guard refuses a tag it cannot see" 1 "v1.9.1: no such tag here"

# ---- release-patch.sh ----

mkdir -p "$work/bin"
cat > "$work/bin/gh" <<EOF
#!/usr/bin/env bash
# Fake gh: answers \`gh run list --branch B\` with one run on B's head on
# the remote, finished as \$FAKE_GH_CONCLUSION (empty: in progress).
branch=""
while [ \$# -gt 0 ]; do
  case "\$1" in --branch) branch="\$2"; shift ;; esac
  shift
done
sha=\$(git --git-dir="$remote" rev-parse "refs/heads/\$branch")
if [ -n "\${FAKE_GH_CONCLUSION:-}" ]; then
  echo "[{\"headSha\":\"\$sha\",\"status\":\"completed\",\"conclusion\":\"\$FAKE_GH_CONCLUSION\"}]"
else
  echo "[{\"headSha\":\"\$sha\",\"status\":\"in_progress\",\"conclusion\":\"\"}]"
fi
EOF
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" RELEASE_PATCH_POLL_SECONDS=0

run "$clone" release-patch.sh v1.0.0 "$fix_b"
check "release-patch refuses a minor release" 1 "v1.0.0 is not a patch release"

run "$clone" release-patch.sh v1.0.1 "$feat_a"
check "release-patch refuses a feat: pick" 1 "is a feature, not a fix: \"feat(ui): add a shiny thing\""
expect "a refused pick makes no branch" bash -c "! git -C '$clone' rev-parse -q --verify refs/heads/release/v1.0.1"

run "$clone" release-patch.sh v1.2.1 "$fix_b"
check "release-patch refuses a tag that exists" 1 "already has the tag v1.2.1"

export FAKE_GH_CONCLUSION=failure
run "$clone" release-patch.sh v1.0.1 "$fix_b" "$fix_d"
check "release-patch pushes the branch and stops on a red release.yml" 1 \
  "pushed release/v1.0.1" "v1.0.0 plus 2 cherry-picks" "release.yml on release/v1.0.1" "finished failure"
expect "the cherry-picks name their originals (-x)" \
  git -C "$clone" log -1 --format=%B release/v1.0.1 --grep "cherry picked from commit $fix_d"
expect "a red release.yml leaves no tag" bash -c "! git --git-dir='$remote' rev-parse -q --verify refs/tags/v1.0.1"

FAKE_GH_CONCLUSION="" RELEASE_PATCH_WAIT_SECONDS=0 run "$clone" release-patch.sh v1.0.1
check "release-patch gives up on a release.yml still running" 1 "is in_progress " "run \`release-patch.sh v1.0.1\` again"

run "$clone" release-patch.sh v1.0.1 "$fix_b"
check "release-patch will not recut a pushed branch" 1 "already has release/v1.0.1"

export FAKE_GH_CONCLUSION=success
run "$clone" release-patch.sh v1.0.1
check "release-patch tags a green release branch" 0 \
  "release.yml is green on release/v1.0.1" "was cut on release/v1.0.1 from v1.0.0; ok" "pushed v1.0.1"
expect "the pushed tag is the release branch's head" \
  test "$(git --git-dir="$remote" rev-parse 'refs/tags/v1.0.1^{commit}')" = "$(git --git-dir="$remote" rev-parse refs/heads/release/v1.0.1)"
expect "latest did not move" bash -c "! git --git-dir='$remote' rev-parse -q --verify refs/tags/latest"

run "$clone" patch-tag-guard.sh v1.0.1
check "guard passes the patch release-patch made" 0 "was cut on release/v1.0.1 from v1.0.0; ok"

# --latest guards what `release-images.sh promote` would publish.
git -C "$clone" tag -f -a latest -m latest "$main" >/dev/null
git -C "$clone" push -q -f origin refs/tags/latest
run "$clone" patch-tag-guard.sh --latest
check "guard --latest refuses the patch latest names" 1 "refusing v1.2.2" "v1.5.1 (" "only fix: commits"
lacks "guard --latest looks only at what latest names" "refusing v1.2.1"
git -C "$clone" tag -f -a latest -m latest "v1.0.1^{commit}" >/dev/null
git -C "$clone" push -q -f origin refs/tags/latest
run "$clone" patch-tag-guard.sh --latest
check "guard --latest passes a proper patch" 0 "was cut on release/v1.0.1 from v1.0.0; ok"

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures of $cases cases failed"
  exit 1
fi
echo "all $cases cases passed"
