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

# verify-release-approval.sh: the person check for a promote-release.yml
# run grain dispatched on a person's Inbox Approve or Reject, or on a
# person's rollback (POST /api/releases/{version}/rollback)
# (design/playbook/releases.playbook.md §0.2 and §11, pkg/releaseapproval).
#
# The run's actor is the deployment's forge credential, so the proof that
# a person decided is the signed record it carries. This verifies the
# record's signature against the deployment's public key and that the
# record is for exactly this run: this repository, this version, this
# decision and short-soak and allow-breaking answers, the commit this repository's
# <version>-pending tag stages (for a rollback: the commit <version> tags,
# and the rollback_to and SHA-256 of the reason the run was given), and
# not expired. On success it prints the
# person who decided, and nothing else, to stdout; on anything else it
# says why on stderr and exits 1.
#
# Environment:
#   APPROVAL             the record, base64 (the `approval` input)
#   APPROVAL_SIGNATURE   its ASN.1 DER ECDSA P-256 SHA-256 signature, base64
#   APPROVAL_PUBLIC_KEY  the deployment's PEM public key
#                        (vars.GRAIN_RELEASE_APPROVAL_KEY)
#   EXPECT_REPO EXPECT_VERSION EXPECT_DECISION EXPECT_SHORT_SOAK_OK
#   EXPECT_ALLOW_BREAKING
#                        what this run was dispatched to do
#   EXPECT_ROLLBACK_TO EXPECT_REASON
#                        rollback only: the rollback_to and reason inputs
#                        (an empty rollback_to is signed as "-")
#   NOW                  seconds since the epoch; tests only (default: now)
# Run in a checkout that has the repository's tags.

set -euo pipefail

refuse() {
  echo "verify-release-approval: refused: $*" >&2
  exit 1
}

[ -n "${APPROVAL_PUBLIC_KEY:-}" ] ||
  refuse "the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set, so no approval from grain's Inbox can be verified: set it to the deployment's key (\`grain release approval-key\`)"
[ -n "${APPROVAL:-}" ] || refuse "no approval record"
[ -n "${APPROVAL_SIGNATURE:-}" ] || refuse "the approval record carries no signature"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
printf '%s\n' "$APPROVAL_PUBLIC_KEY" >"$work/key.pem"
printf '%s' "$APPROVAL" | base64 -d >"$work/record" 2>/dev/null || refuse "the approval record is not base64"
printf '%s' "$APPROVAL_SIGNATURE" | base64 -d >"$work/sig" 2>/dev/null || refuse "the signature is not base64"
openssl dgst -sha256 -verify "$work/key.pem" -signature "$work/sig" "$work/record" >/dev/null 2>&1 ||
  refuse "the approval record's signature does not verify against GRAIN_RELEASE_APPROVAL_KEY"

# Signed, so from here on the record says what the deployment signed.
[ "$(head -n 1 "$work/record")" = "grain-release-approval/v1" ] || refuse "not a grain-release-approval/v1 record"
field() { sed -nE "s/^$1: (.*)\$/\\1/p" "$work/record" | head -n 1; }
want() { # want <field> <expected>
  local got
  got="$(field "$1")"
  [ "$got" = "$2" ] || refuse "the record's $1 is \"$got\", but this run is for \"$2\""
}
want repo "${EXPECT_REPO:-}"
want version "${EXPECT_VERSION:-}"
want decision "${EXPECT_DECISION:-}"
want short_soak_ok "${EXPECT_SHORT_SOAK_OK:-false}"
want allow_breaking "${EXPECT_ALLOW_BREAKING:-false}"

now="${NOW:-$(date +%s)}"
issued="$(field issued)"
expires="$(field expires)"
[[ "$issued" =~ ^[0-9]+$ && "$expires" =~ ^[0-9]+$ ]] || refuse "the record's times are not numbers"
[ "$issued" -le $((now + 60)) ] || refuse "the record was issued in the future"
[ "$now" -lt "$expires" ] || refuse "the record expired at $expires (now $now): press Approve again"

if [ "${EXPECT_DECISION:-}" = rollback ]; then
  want rollback_to "${EXPECT_ROLLBACK_TO:--}"
  expect_reason_sha="$(printf '%s' "${EXPECT_REASON:-}" | openssl dgst -sha256 | sed -E 's/^.*= ?//')"
  want reason_sha256 "$expect_reason_sha"
  released="$(git rev-parse -q --verify "refs/tags/${EXPECT_VERSION}^{commit}" 2>/dev/null || true)"
  [ -n "$released" ] || refuse "${EXPECT_VERSION} is not tagged here"
  want commit "$released"
else
  pending="$(git rev-parse -q --verify "refs/tags/${EXPECT_VERSION}-pending^{commit}" 2>/dev/null || true)"
  [ -n "$pending" ] || refuse "${EXPECT_VERSION}-pending is not tagged here"
  want commit "$pending"
fi

approver="$(field approver)"
[ -n "$approver" ] || refuse "the record names no approver"
printf '%s\n' "$approver"
