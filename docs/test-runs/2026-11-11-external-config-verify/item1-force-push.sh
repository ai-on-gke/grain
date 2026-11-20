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

# item1-force-push.sh: a force-push that rewrites the branch from two
# commits back with a *signed* commit (one that would verify on its own)
# is still held, because the last applied commit is no longer on it;
# accepting the rewritten tip by its SHA applies it.
here=$(dirname "$0")
. "$here/lib.sh"
before=$(state)
git -C "$W/clone" reset -q --hard HEAD~2
r=$(direct README.md "rewritten history, signed" sign)
push --force
echo "rewritten tip=$r"
wait_sync
echo "after force-push: $(state)"
grep -c "require_verified: holding ${r:0:12}" "$W/daemon.log" || true
set_remote "$(jq -nc --arg s "$r" '{acceptCommit:$s}')" >/dev/null
wait_sync
echo "after accepting the tip: $(state)"
