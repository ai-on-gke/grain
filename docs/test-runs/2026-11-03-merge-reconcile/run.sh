#!/usr/bin/env bash
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
