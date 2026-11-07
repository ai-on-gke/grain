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

# tamper.sh DISPATCHES_JSON INDEX PUBLIC_KEY_PEM BARE: §2 item 2. Takes
# one promote-release.yml dispatch the demo really sent (signed with its
# real key), clones the simulated repository it was for, and runs the
# verifier the workflow runs (scripts/verify-release-approval.sh) over
# it as dispatched and then expired, against another commit, against
# another key, signed by another key, and with an edited field. Only the
# first may pass.
set -uo pipefail
dispatches="${1:?dispatches json}"
index="${2:?index}"
pub="${3:?public key pem}"
bare="${4:?bare repo}"
repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
verify="$repo_root/scripts/verify-release-approval.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git clone -q "$bare" "$work/clone" 2>/dev/null
# The dispatch's inputs, as IN_<input> shell variables.
IN_version="" IN_decision="" IN_approval="" IN_approval_signature="" IN_rollback_to="" IN_reason=""
IN_short_soak_ok="" IN_allow_breaking=""
eval "$(python3 - "$dispatches" "$index" <<'EOF'
import json, shlex, sys
d = json.load(open(sys.argv[1]))[int(sys.argv[2])]["inputs"]
for k in ("version", "decision", "approval", "approval_signature", "rollback_to", "reason",
          "short_soak_ok", "allow_breaking"):
    print(f"IN_{k}={shlex.quote(d.get(k, ''))}")
EOF
)"
printf '%s' "$IN_approval" | base64 -d >"$work/record"
expires="$(sed -nE 's/^expires: //p' "$work/record")"
echo "record under test ($IN_decision $IN_version):"
sed 's/^/  /' "$work/record"

run() { # run <label> [VAR=value ...]
  local label="$1"; shift
  local out rc
  out="$(cd "$work/clone" && env APPROVAL="$IN_approval" APPROVAL_SIGNATURE="$IN_approval_signature" \
    APPROVAL_PUBLIC_KEY="$(cat "$pub")" EXPECT_REPO=acme/widgets EXPECT_VERSION="$IN_version" \
    EXPECT_DECISION="$IN_decision" EXPECT_SHORT_SOAK_OK="${IN_short_soak_ok:-false}" \
    EXPECT_ALLOW_BREAKING="${IN_allow_breaking:-false}" EXPECT_ROLLBACK_TO="$IN_rollback_to" \
    EXPECT_REASON="$IN_reason" "$@" bash "$verify" 2>&1)"
  rc=$?
  printf '%-34s exit %d: %s\n' "$label" "$rc" "$out"
}

run "as dispatched"
run "expired (NOW = expires)" NOW="$expires"
run "expired (NOW = expires + 3600)" NOW="$((expires + 3600))"

# Another commit: the tag the run checks out stages a different commit.
tag="$IN_version-pending"
[ "$IN_decision" = rollback ] && tag="$IN_version"
(cd "$work/clone" && git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m other &&
  git tag -f "$tag" HEAD >/dev/null 2>&1)
run "another commit ($tag moved)"
(cd "$work/clone" && git fetch -q -f origin "refs/tags/$tag:refs/tags/$tag")

# Another key: the repository variable names a key the deployment never held.
openssl ecparam -name prime256v1 -genkey -noout -out "$work/other.key" 2>/dev/null
openssl ec -in "$work/other.key" -pubout -out "$work/other.pub" 2>/dev/null
run "verified against another key" APPROVAL_PUBLIC_KEY="$(cat "$work/other.pub")"

# Signed with another key: the same record text, signed by somebody else.
openssl dgst -sha256 -sign "$work/other.key" -out "$work/other.sig" "$work/record"
run "signed with another key" APPROVAL_SIGNATURE="$(base64 -w0 "$work/other.sig")"

# Edited: the approver swapped after signing.
sed 's/^approver: .*/approver: mallory@example.com/' "$work/record" >"$work/edited"
run "approver edited after signing" APPROVAL="$(base64 -w0 "$work/edited")"

if [ "$IN_decision" = rollback ]; then
  # The run's inputs edited after dispatch: another reason, another target.
  run "rollback: reason input changed" EXPECT_REASON="a different reason"
  run "rollback: rollback_to changed" EXPECT_ROLLBACK_TO="v2.2.0"
fi
