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

# agentd-compat.sh: run this tree's daemon side of the host agent contract
# against grain-agentd as earlier releases shipped it
# (design/design/agentd-stable-contract.design.md, "Guard rails").
#
# For each release tag it builds cmd/grain-agentd from that tag's own
# source (a detached worktree, so the tag's go.mod and dependencies), plus
# this tree's, then runs tests/agentdcompat with every binary: start,
# detached start, reattach after a daemon restart, cancel, stage, listen
# and drain must all still work.
#
# The tags are $AGENTD_COMPAT_TAGS when set (space-separated); otherwise the
# oldest release still accepted ($AGENTD_COMPAT_OLDEST, the first release
# that shipped grain-agentd) and every final vX.Y.Z release after it, up to
# the newest $AGENTD_COMPAT_LAST (default 5) of them. A tag missing from the
# clone fails the run: CI must fetch tags (fetch-depth: 0).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

OLDEST="${AGENTD_COMPAT_OLDEST:-v3.2.0}"
LAST="${AGENTD_COMPAT_LAST:-5}"

version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; }

if [ -n "${AGENTD_COMPAT_TAGS:-}" ]; then
	read -r -a tags <<<"$AGENTD_COMPAT_TAGS"
else
	if ! git rev-parse -q --verify "refs/tags/$OLDEST" >/dev/null; then
		echo "agentd-compat: tag $OLDEST is not in this clone; fetch tags (git fetch --tags, or fetch-depth: 0)" >&2
		exit 1
	fi
	mapfile -t finals < <(git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V)
	newer=()
	for t in "${finals[@]}"; do
		if [ "$t" != "$OLDEST" ] && version_ge "$t" "$OLDEST"; then
			newer+=("$t")
		fi
	done
	tags=("$OLDEST")
	if [ "${#newer[@]}" -gt "$LAST" ]; then
		newer=("${newer[@]: -$LAST}")
	fi
	tags+=("${newer[@]}")
fi

out="$(mktemp -d "${TMPDIR:-/tmp}/agentd-compat.XXXXXX")"
trap 'for w in "$out"/wt-*; do [ -d "$w" ] && git worktree remove --force "$w" >/dev/null 2>&1; done; rm -rf "$out"' EXIT

ldflags() { echo "-X github.com/ai-on-gke/grain/pkg/version.Release=$1"; }

bins=()
for tag in "${tags[@]}"; do
	wt="$out/wt-$tag"
	echo "agentd-compat: building grain-agentd $tag"
	git worktree add --detach --quiet "$wt" "refs/tags/$tag"
	if [ ! -f "$wt/cmd/grain-agentd/main.go" ]; then
		echo "agentd-compat: $tag has no cmd/grain-agentd; raise AGENTD_COMPAT_OLDEST past it" >&2
		exit 1
	fi
	(cd "$wt" && CGO_ENABLED=0 go build -trimpath -ldflags "$(ldflags "$tag")" -o "$out/grain-agentd-$tag" ./cmd/grain-agentd)
	git worktree remove --force "$wt"
	bins+=("$tag=$out/grain-agentd-$tag")
done

echo "agentd-compat: building grain-agentd from this tree"
CGO_ENABLED=0 go build -trimpath -ldflags "$(ldflags head)" -o "$out/grain-agentd-head" ./cmd/grain-agentd
bins+=("head=$out/grain-agentd-head")

list="$(IFS=,; echo "${bins[*]}")"
echo "agentd-compat: testing $list"
GRAIN_AGENTD_COMPAT_BINARIES="$list" go test -count=1 -v ./tests/agentdcompat "$@"
