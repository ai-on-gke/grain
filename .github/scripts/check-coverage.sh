#!/bin/bash
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

set -e

# Double check if skip coverage environment variable is set
if [ "${SKIP_COVERAGE}" = "true" ]; then
  echo "test/skip-coverage label or override is set. Skipping coverage enforcement."
  exit 0
fi

# Determine maximum allowable regression, defaulting to 1.0% if not specified
MAX_REGRESSION=${MAX_REGRESSION:-1.0}
echo "Maximum allowable coverage regression: ${MAX_REGRESSION}%"

# Ensure we are in the workspace root
# When the GHA step runs "bash pr/.github/scripts/check-coverage.sh",
# the current directory is the GHA workspace root (which contains 'pr' and 'base' folders).

if [ ! -d "base" ] || [ ! -d "pr" ]; then
  echo "Error: 'base' or 'pr' directory not found. Make sure both paths are checked out."
  exit 1
fi

echo "Calculating coverage for base branch..."
cd base
go mod download
go test -coverprofile=coverage.out ./...
BASE_COV=$(go tool cover -func=coverage.out | grep '^total:' | awk '{print $NF}' | sed 's/%//')
cd ..

echo "Calculating coverage for PR branch..."
cd pr
go mod download
go test -coverprofile=coverage.out ./...
PR_COV=$(go tool cover -func=coverage.out | grep '^total:' | awk '{print $NF}' | sed 's/%//')
cd ..

# Validate coverage values
if ! [[ "$BASE_COV" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
  echo "Warning: Could not parse base coverage ('$BASE_COV'), defaulting to 0.0"
  BASE_COV="0.0"
fi
if ! [[ "$PR_COV" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
  echo "Warning: Could not parse PR coverage ('$PR_COV'), defaulting to 0.0"
  PR_COV="0.0"
fi

echo "Base Coverage: ${BASE_COV}%"
echo "PR Coverage: ${PR_COV}%"

# Calculate minimum required coverage
MIN_REQUIRED_COV=$(awk "BEGIN {print $BASE_COV - $MAX_REGRESSION}")
echo "Minimum required coverage: ${MIN_REQUIRED_COV}%"

# Compare PR coverage with minimum required coverage
if awk "BEGIN {exit !($PR_COV >= $MIN_REQUIRED_COV)}"; then
  echo "Success: PR coverage is acceptable."
  exit 0
else
  echo "Error: PR coverage (${PR_COV}%) is lower than base coverage (${BASE_COV}%) by more than the allowed regression of ${MAX_REGRESSION}%."
  exit 1
fi
