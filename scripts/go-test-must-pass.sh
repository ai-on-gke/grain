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

# Run one opt-in Go test and fail unless that test actually passed.
#
#   scripts/go-test-must-pass.sh TestName [go test flags and packages...]
#
# The stress suites behind `make loadtest`, `make scalability` and the
# rest skip unless their own env var is set, and `go test -run X` over a
# name that matches nothing exits 0 with "no tests to run". Either way
# the nightly job that runs the target would be green over nothing: a
# renamed test, a dropped env var or a new skip condition all look like
# a pass. This runs `go test -v -run '^TestName$' ...`, keeps its exit
# status, and then also requires a top-level "--- PASS: TestName" line,
# the same check image.yml makes for tests/container.
set -euo pipefail

if [ "$#" -lt 1 ]; then
	echo "usage: $0 TestName [go test args...]" >&2
	exit 2
fi
name="$1"
shift

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

go test -count=1 -v -run "^${name}\$" "$@" 2>&1 | tee "$log"

if grep -q -- "^--- SKIP: ${name} " "$log"; then
	echo "${name} skipped: this target exists to run it, so a skip is a missing verdict rather than a pass." >&2
	exit 1
fi
if ! grep -q -- "^--- PASS: ${name} " "$log"; then
	echo "${name} did not report a pass (no test matched, or it never finished), so a green exit here would mean nothing." >&2
	exit 1
fi
