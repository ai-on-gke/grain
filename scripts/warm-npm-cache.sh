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

# Fill npm's cache with everything a directory's package-lock.json names,
# and -- when that does not work -- decide whose problem it was.
#
# This is the fetch half of `npm ci`, split off from the check half
# (grain/task-498). The job that runs this runs it in a step of its own
# and then installs from the cache alone (`npm_config_offline`), so that
# the two halves fail in two different ways: a registry that will not
# answer errors the job, and a lockfile that does not resolve fails it.
#
# "Errors" rather than "fails" is `infrastructure: true`, which
# .grain/workflows/tests.yml declares on the step that runs this.
# Nothing in this script depends on that key: this script's job is to
# fetch, and to say whose fault it is when it could not.
#
# Why the split is needed at all is pkg/ci.Step.Infrastructure's own
# doc comment, which names this script's problem as the near miss worth
# naming: a registry that will not answer is this deployment's problem
# and must not redden a branch, a lockfile that does not resolve is the
# commit's problem and must, and `npm ci` exits 1 either way. Declaring
# the whole step `infrastructure:` would hide real breakage behind a
# verdict nobody blames, which is worse than the bug it fixes; declaring
# none of it leaves every branch one npm outage away from a red tick it
# did not earn.
#
# So the fetch happens here, into a throwaway directory -- nothing this
# script writes is what anything is later built from, and the only thing
# it leaves behind is a warm ~/.npm -- and the install that matters
# happens in the step after it, offline, against what this one fetched.
# An offline install cannot fail on the network, so its failure is
# unambiguously the tree's. That is the whole of the arrangement.
#
# What this script has to decide is the one question the split leaves
# open: `npm ci` here did not succeed, so is the cache incomplete
# because the registry would not answer, or because this commit's
# lockfile names something that is not there? Two readings, in this
# order, and the polarity of both is deliberate:
#
#   - npm's own error code, when it named one this script recognises as
#     a transport failure (no route, no answer, a timeout, a 5xx, a
#     429). Those are the deployment's.
#   - `npm ping`, otherwise. A registry that does not answer a ping
#     right now did not answer the install either, whatever npm called
#     it -- which is the reading that catches the codes this script has
#     never seen, and the npm bug where a failed install exits 0 with
#     `npm error Exit handler never called!` in its output.
#
# Anything else exits 0 and says why: npm failed, the registry is up,
# and so the failure is this commit's to answer for. The verdict is then
# the offline install's to give, which is exactly where a verdict
# belongs. That is the safe direction to be wrong in -- an unrecognised
# *infrastructure* failure costs a red tick somebody has to look at,
# while an unrecognised *tree* failure would cost a green one nobody
# does -- and it is why this script never swallows a failure it cannot
# explain.
#
# Usage: scripts/warm-npm-cache.sh [directory]   (default: ui)
#
# Exit status is the whole of the interface:
#
#   0  npm's cache now holds what the lockfile names -- or npm failed
#      for a reason that is this commit's, and the verdict belongs to
#      the install that follows rather than to this step.
#   1  the registry did not answer. Nothing was learned about the tree,
#      and the job this runs in should end up `errored` rather than
#      `failure` -- which is what `infrastructure: true` on the step
#      arranges.

# No `set -e`: the whole point of this script is that it has something
# to say about a command that failed. `pipefail` so that `npm ... | tee`
# reports npm's status rather than tee's.
set -uo pipefail

dir="${1:-ui}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# This half fetches, so it is online whatever the job around it says.
#
# The job that runs this sets npm's offline mode for *every* step of it
# (.grain/workflows/tests.yml's `install-offline`, which is a job-level
# `env:` because the installs it is meant for are inside `make frontend`
# rather than written out as npm commands). That env reaches this step
# too, and an offline `npm ci` here serves from the very cache this
# script exists to fill: it fetches nothing, fails ENOTCACHED on the
# first package the cache is missing, and -- since ENOTCACHED is not a
# transport code and the registry pings fine -- this script then reads
# its own muzzling as "the lockfile does not resolve" and hands the
# verdict to the install below, which fails identically. A red frontend
# job for a tree with nothing wrong with it.
#
# It stayed invisible while the guest image shipped a warm ~/.npm, which
# made both halves cache hits; the Granulefile the image is built from
# today warms nothing, so the first commit to test on it went red.
# Unset rather than merely set to false, and both spellings of it,
# because npm reads either case from the environment.
unset NPM_CONFIG_OFFLINE
export npm_config_offline=false

if [ ! -f "${root}/${dir}/package-lock.json" ]; then
  echo "warm-npm-cache.sh: ${dir}/package-lock.json does not exist, so there is nothing to fetch." >&2
  exit 1
fi

# A registry that has not answered an install is not going to answer
# five retries backing off as far as two minutes each -- and this
# question is asked only after an install has already failed, so the
# answer is wanted in seconds rather than in the ten minutes the guest
# image's own fetch-retry settings would spend on it
# (the repository's Granulefile, which sets them for the install rather
# than for this).
registry_answers() {
  npm_config_fetch_retries=2 \
    npm_config_fetch_retry_mintimeout=1000 \
    npm_config_fetch_retry_maxtimeout=10000 \
    npm ping >/dev/null 2>&1
}

# package.json and package-lock.json copied out into a directory of
# their own: `npm ci` needs somewhere to install into, and this script must not be the thing that writes the
# node_modules the next step is judged on. What is kept is the cache the
# install fills on its way, which is shared and is the point.
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
cp "${root}/${dir}/package.json" "${root}/${dir}/package-lock.json" "${work}/" || exit 1

log="${work}/npm-ci.log"
echo "warm-npm-cache.sh: filling npm's cache with what ${dir}/package-lock.json names, from $(npm config get registry)"
(cd "${work}" && npm ci --no-audit --no-fund) 2>&1 | tee "${log}"
status="${PIPESTATUS[0]}"

# `npm error` in the output as well as the exit status, because npm has
# been seen to exit 0 from an install that did not finish -- "Exit
# handler never called!", npm/cli's own bug report line. A warm step
# that believed that exit code would hand the offline install a cache
# with holes in it, and the red tick for a network failure would land on
# the commit after all.
if [ "${status}" -eq 0 ] && ! grep -q '^npm error' "${log}"; then
  echo "warm-npm-cache.sh: npm's cache now holds what ${dir}/package-lock.json names."
  exit 0
fi

# The transport codes: npm could not get an answer, or got one that says
# the registry could not serve this request rather than that the request
# was wrong. E404 and E403 are deliberately *not* here -- a registry that
# says "no such package" has answered, and what it answered about is the
# lockfile.
transport='^npm (error|ERR!) (code (E5[0-9][0-9]|E429|E408|EAI_AGAIN|ENOTFOUND|ECONNREFUSED|ECONNRESET|ETIMEDOUT|EHOSTUNREACH|ENETUNREACH|EPIPE|EPROTO|ERR_SOCKET_TIMEOUT|ERR_STREAM_PREMATURE_CLOSE|FETCH_ERROR)$|network )'
if grep -Eq "${transport}" "${log}"; then
  reason="npm named a transport failure above"
elif ! registry_answers; then
  reason="npm named no transport failure, and $(npm config get registry) does not answer a ping either"
else
  cat >&2 <<EOF

warm-npm-cache.sh: npm could not install ${dir}/package-lock.json, and the
registry is answering -- so this is the lockfile's problem rather than the
network's, and it is not this step's to report. Exiting 0 and leaving the
verdict to the offline install that follows, which will fail the same way
and will fail as what it is: a commit whose dependencies do not resolve.
EOF
  exit 0
fi

cat >&2 <<EOF

warm-npm-cache.sh: npm could not fill its cache for ${dir}: ${reason}.
That is this deployment's problem and not this commit's, so this step is
declared \`infrastructure:\` and the job it is in ends up errored rather
than failed: nothing here is a verdict about the tree. See
pkg/ci.Step.Infrastructure.
EOF
exit 1
