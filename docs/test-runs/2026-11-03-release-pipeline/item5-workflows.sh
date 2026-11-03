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

# §5 item 5: actionlint on the release workflows, nightly.yml's tag filter
# against tag names the train makes, and stage-1's needs against the
# playbook's Stage 1 table and candidate-gate.sh's list.
#
#   bash item5-workflows.sh <path-to-grain-checkout>
set -uo pipefail
cd "$1" || exit 1

echo "== actionlint $(actionlint -version | head -n 1) (shellcheck $(shellcheck --version | sed -n 's/^version: //p'))"
actionlint .github/workflows/nightly.yml .github/workflows/release.yml .github/workflows/release-train.yml \
  .github/workflows/promote.yml .github/workflows/promote-release.yml
echo "== exit $?"

# GitHub's filter glob: * is any run of characters but /; a pattern
# starting with ! excludes what the patterns before it included.
include=() exclude=()
while IFS= read -r p; do
  case "$p" in '!'*) exclude+=("${p#!}") ;; *) include+=("$p") ;; esac
done < <(sed -n '/^  push:/,/^  [a-z]/p' .github/workflows/nightly.yml | sed -n 's/^ *- "\(.*\)"$/\1/p')
echo "== nightly.yml push tags: include ${include[*]}; exclude ${exclude[*]}"
glob_re() { printf '^%s$' "$(printf '%s' "$1" | sed 's/[.]/\\./g; s/\*/[^\/]*/g')"; }
fires() {
  local t="$1" p hit=no
  for p in "${include[@]}"; do [[ "$t" =~ $(glob_re "$p") ]] && hit=yes; done
  for p in "${exclude[@]}"; do [[ "$t" =~ $(glob_re "$p") ]] && hit=no; done
  echo "$hit"
}
bad=0
while read -r tag want; do
  got="$(fires "$tag")"
  git check-ref-format "refs/tags/$tag" || { echo "   $tag is not a valid tag name"; continue; }
  mark=ok
  [ "$got" = "$want" ] || { mark=WRONG; bad=1; }
  printf '   %-6s %-32s fires=%s (want %s)\n' "$mark" "$tag" "$got" "$want"
done <<'EOF'
v3.2.0-rc.1 yes
v3.2.0-rc.12 yes
v10.0.0-rc.3 yes
v3.2.0-rc.1-discarded no
v3.2.0-rc.1-retracted no
v3.2.0-pending no
v3.2.0 no
v3.2.1 no
v3.2.0-retracted no
latest no
EOF
echo "== tag filter: $([ $bad = 0 ] && echo as intended || echo WRONG)"

needs="$(sed -n '/^  stage-1:/,/^    runs-on:/p' .github/workflows/nightly.yml | sed -n 's/^      - \([a-z0-9-]*\)$/\1/p' | sort)"
playbook="$(grep -m1 '^| `go-long`' design/playbook/releases.playbook.md | cut -d'|' -f2 | grep -o '`[a-z0-9-]*`' | tr -d '`' | sort)"
gate="$(sed -n '/^CHECK_QUALIFICATION_JOBS=/,/}"$/p' scripts/candidate-gate.sh | sed 's/^CHECK_QUALIFICATION_JOBS="${CHECK_QUALIFICATION_JOBS-//' | sed -n 's/^\([a-z0-9-]*\)=.*/\1/p' | sort)"
echo "== stage-1 needs ($(echo "$needs" | wc -l)): $(echo "$needs" | paste -sd' ')"
echo "== playbook Stage 1 'Required now' row ($(echo "$playbook" | wc -l)): $(echo "$playbook" | paste -sd' ')"
echo "== candidate-gate.sh Stage 1 list ($(echo "$gate" | grep -c .)): $(echo "$gate" | paste -sd' ')"
diff <(echo "$needs") <(echo "$playbook") && echo "   needs == playbook"
[ -z "$gate" ] || { diff <(echo "$needs") <(echo "$gate") && echo "   needs == candidate-gate.sh"; }
echo "== every job stage-1 needs is a job of nightly.yml:"
for j in $needs; do grep -q "^  $j:$" .github/workflows/nightly.yml && echo "   ok $j" || echo "   MISSING $j"; done
echo "== nightly.yml jobs not in stage-1:"
sed -n '/^jobs:/,$p' .github/workflows/nightly.yml | grep -o '^  [a-z0-9-]*:$' | tr -d ' :' | grep -vx stage-1 | while read -r j; do
  echo "$needs" | grep -qx "$j" || echo "   $j"
done
echo "== concurrency:"
echo "   $(grep -c '^    concurrency:' .github/workflows/nightly.yml) job concurrency groups, keyed on github.ref;" \
  "$(grep -c "cancel-in-progress: \${{ github.ref != 'refs/heads/main' && !startsWith(github.ref, 'refs/tags/v') }}" .github/workflows/nightly.yml)" \
  "never cancel on main or a v* tag"
