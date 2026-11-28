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

# item1-sequence.sh: §11 item 1's commit sequence. Turns require_verified
# on (one approval, grain-merge[bot] as grain's forge account, a
# throwaway SSH key in allowed_signers), then pushes in one go: an
# approved PR merge, a grain merge, an SSH-signed commit, an unsigned
# direct push, another approved merge. Prints what the daemon applied
# and holds.
here=$(dirname "$0")
. "$here/lib.sh"

[ -f "$W/signing_key" ] || ssh-keygen -q -t ed25519 -N "" -C walk@example.com -f "$W/signing_key"
signers="walk@example.com $(cut -d' ' -f1-2 "$W/signing_key.pub")"
set_remote "$(jq -nc --arg s "$signers" \
	'{requireVerified:true, requiredApprovals:1, verifiedMergers:"grain-merge[bot]", allowedSigners:$s}')" |
	jq -c '.verification'
wait_sync
echo "before: $(state)"

read -r m1 h1 < <(pr_merge pr1 README.md "approved change one")
add_pull 1 "$m1" "$h1" bob bob '[{"user":"carol","state":"APPROVED","commit_id":"'"$h1"'","author_association":"MEMBER"}]'
read -r m2 h2 < <(pr_merge pr2 README.md "merged by grain")
add_pull 2 "$m2" "$h2" bob 'grain-merge[bot]' '[]'
s3=$(direct README.md "signed by walk" sign)
u4=$(direct README.md "unsigned direct push")
read -r m5 h5 < <(pr_merge pr5 README.md "approved change five")
add_pull 5 "$m5" "$h5" bob bob '[{"user":"carol","state":"APPROVED","commit_id":"'"$h5"'","author_association":"MEMBER"}]'
push
printf 'm1=%s\nm2=%s\ns3=%s\nu4=%s\nm5=%s\n' "$m1" "$m2" "$s3" "$u4" "$m5" | tee "$W/item1.shas"
wait_sync
echo "after: $(state)"
echo "journal lines naming the hold:"
grep -c "require_verified: holding ${u4:0:12}" "$W/daemon.log" || true
