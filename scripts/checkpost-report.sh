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

# Report one CI verdict to a checkpost service (pkg/checkpost).
#
# This is the client side of taking grain's merge queue off the GitHub
# API: instead of the queue asking GitHub every thirty seconds how a
# build ended, the build says so once, here. See
# docs/leaving-the-github-api.md.
#
# It is shell rather than the `checkpost report` subcommand because the
# job that calls it is the last step of a workflow whose other jobs have
# already finished -- installing a Go toolchain and building a binary to
# send one 200-byte request would cost more than everything it is
# reporting on. The signing is therefore written twice, once here and
# once in pkg/checkpost/sign.go, and that duplication is deliberately
# held in place by a test: pkg/checkpost's TestTheShellReporterSignsThe
# SameWayGoDoes runs this script against a real server and fails if the
# two ever disagree.
#
# Usage, all through the environment so nothing sensitive reaches the
# process table:
#
#   CHECKPOST_URL     base url of the service
#   CHECKPOST_SECRET  the shared secret, as a repository secret
#   REPO              owner/name
#   COMMIT            the full 40-character sha the checks ran against
#   NAME              the check's name, as a person sees it
#   CONCLUSION        success | failure | cancelled | skipped | ...
#   CHECK_URL         where a person goes to look (optional)
#   ATTEMPT           the workflow run attempt, GitHub's run_attempt
#                     (optional). A re-run's success replaces a failure
#                     already reported only when its attempt is higher.
#
# It exits non-zero if the report could not be delivered, so a workflow
# that silently stopped reporting shows up as a failing job rather than
# as a merge queue that quietly went back to waiting on nothing.

set -euo pipefail

for var in CHECKPOST_URL CHECKPOST_SECRET REPO COMMIT NAME CONCLUSION; do
  if [ -z "${!var:-}" ]; then
    echo "checkpost-report: $var is not set" >&2
    exit 2
  fi
done

# jq builds the body rather than printf, so that a check name containing
# a quote or a backslash -- which a workflow's `name:` may perfectly well
# contain -- produces valid JSON instead of a 400 nobody can read.
body=$(jq -cn \
  --arg repo "$REPO" \
  --arg commit "$COMMIT" \
  --arg name "$NAME" \
  --arg conclusion "$CONCLUSION" \
  --arg url "${CHECK_URL:-}" \
  --arg attempt "${ATTEMPT:-}" \
  '{repo: $repo, commit: $commit, name: $name, conclusion: $conclusion}
   + (if $url == "" then {} else {url: $url} end)
   + (if $attempt == "" then {} else {attempt: ($attempt | tonumber)} end)')

ts=$(date +%s)

# The signature covers "<timestamp>.<METHOD> <path>.<body>". The
# timestamp pins a request to a moment: without it, anybody who captured
# one could repost the verdict it carried for as long as the secret lived,
# and the one most worth reposting is a success. The method and path pin
# it to this route (pkg/checkpost/sign.go says why that matters for a
# read). The path is the route the service sees, so it stays
# /v1/verdicts whatever prefix CHECKPOST_URL carries in front of it.
#
# -hmac takes the secret as an argument, so it is visible in this
# process's own argv for the life of one openssl invocation. That is the
# one exposure here and it is accepted: the alternative, -macopt
# hexkey:..., needs the secret hex-encoded first and puts it in argv
# just the same. Nothing else on a GitHub runner is reading argv.
signature=$(printf '%s.%s %s.%s' "$ts" POST /v1/verdicts "$body" \
  | openssl dgst -sha256 -hmac "$CHECKPOST_SECRET" -r \
  | cut -d' ' -f1)

# --fail-with-body so a 4xx exits non-zero *and* prints what the service
# said, which for a malformed verdict names the field that is wrong.
# Retried because this runs once at the end of a build: a dropped
# connection here loses the verdict for a commit nothing will report on
# again, and the queue then waits on silence.
curl --silent --show-error --fail-with-body \
  --retry 5 --retry-connrefused --retry-delay 2 --max-time 30 \
  -X POST "${CHECKPOST_URL%/}/v1/verdicts" \
  -H 'Content-Type: application/json' \
  -H "X-Checkpost-Timestamp: $ts" \
  -H "X-Checkpost-Signature: $signature" \
  --data-raw "$body"
