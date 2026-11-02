// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Merge on green (model.RepoConfig.MergeOnGreen, grain/task-545): a
// repository that asks for it has a green, clean change merge past an
// entry ahead of it whose CI is still running, rather than wait its turn.
package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// headInCIBeforeGreen is a lane of two: t1 at the front with its grain CI
// still running, t2 behind it green and clean.
func headInCIBeforeGreen(t *testing.T, ctx context.Context, store *model.Store,
	fix *simFixture, runner *fakeCI) (headBranch, behindBranch string) {

	t.Helper()
	head, headBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t1", fix.repo, "main")
	head = grantCI(t, ctx, store, head)
	startRequiredCI(t, ctx, store, fix.client, runner, head, fix.repo, headBranch)
	behind, behindBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t2", fix.repo, "main")
	behind = grantCI(t, ctx, store, behind)
	greenGrainCI(t, ctx, store, fix.client, runner, behind, fix.repo, behindBranch)
	setMergeable(fix.sim, true)
	return headBranch, behindBranch
}

func mergeOnGreen(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, MergeOnGreen: true}); err != nil {
		t.Fatal(err)
	}
}

func TestMergeOnGreenMergesAGreenChangePastAHeadStillInCI(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()
	headBranch, behindBranch := headInCIBeforeGreen(t, ctx, store, fix, runner)
	mergeOnGreen(t, ctx, store, fix.repo)
	cfg := model.DefaultConfig()
	cfg.MergeBatchSize = 1
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if !mergedBranch(fix.sim, behindBranch) {
		t.Fatal("the green change did not merge past a head whose CI was still running")
	}
	if mergedBranch(fix.sim, headBranch) {
		t.Fatal("the head merged while its own CI was still running")
	}
	if state, note := mergeQueueStateOf(t, ctx, store, "t1"); state != "waiting_ci" {
		t.Fatalf("the passed-over head says %q (%q), want waiting_ci", state, note)
	}
}
