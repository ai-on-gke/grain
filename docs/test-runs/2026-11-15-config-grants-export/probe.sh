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

# probe.sh, run inside a task's sandbox by the fake agent: report which
# repositories the run was handed and, through the task's own git proxy
# URL, which ones it can clone and which it can push its branch to.
set -u
origin=$(git -C work remote get-url origin)
branch=$(git -C work branch --show-current)
echo "branch=$branch"
for d in $(find . -mindepth 2 -maxdepth 4 -name .git -not -path ./work/.git | sed 's|/\.git$||' | sort); do
	echo "handed: $d -> $(git -C "$d" log --oneline -1 2>&1 | head -1)"
done
for repo in docs gadgets; do
	url=${origin/acme\/widgets/acme\/$repo}
	dir=probe-$repo
	rm -rf "$dir"
	if git clone -q "$url" "$dir" 2>/dev/null; then
		echo "clone acme/$repo: ok"
		git -C "$dir" checkout -q -b "$branch"
		echo "$branch" >"$dir/walk.txt"
		git -C "$dir" add walk.txt
		git -C "$dir" -c user.name=walk -c user.email=walk@example.com commit -qm "chore: walk $repo"
		if out=$(git -C "$dir" push -q origin "HEAD:refs/heads/$branch" 2>&1); then
			echo "push acme/$repo $branch: ok"
		else
			echo "push acme/$repo $branch: refused: $(echo "$out" | grep -v '^\s*$' | head -2 | tr '\n' ' ')"
		fi
	else
		echo "clone acme/$repo: refused"
	fi
done
