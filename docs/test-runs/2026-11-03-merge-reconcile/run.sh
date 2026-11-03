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

# Runs every test docs/test-runs/2026-11-03-merge-reconcile.md cites, from
# the repository root.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
go test ./tests/e2e/ -run 'TestMergeReconcile' -count=1 -v
go test ./pkg/daemon/ -count=1 -v -run \
  'TestNodeMutationsOnASecondRESTHost|TestGrainPRResolveOnAForgeCLIHost|TestResolveReviewThreadOnAForgeCLI|TestForgeRouterRoutesANodeMutationByItsRepoHeader'
go test ./pkg/ui/ -count=1 -run 'TestCIRunsList'
go test ./pkg/orchestrator/ -count=1 -run \
  'TestStalledExternalCheck|TestInFlightPausedAndAwaitingSubmit|TestAPullRequest.*AfterItsTaskClosed'
