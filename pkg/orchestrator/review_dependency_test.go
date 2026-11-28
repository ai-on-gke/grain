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

package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A re-review chained behind an older review of the same pull request
// stops waiting once that older review has completed -- its verdict is
// recorded -- even though the fix pull request it opened keeps it open.
// Reviews 4716, 4738, 4807, 5047, 5067, 5170 and 5187 each sat queued for
// days behind a review like that (task 6236), because a depends_on link
// clears only when its target closes. The older review is left open to
// land its fix, and an incremental re-review is widened to the whole
// change, since the fix it would have read past has not landed.
func TestAReReviewStopsWaitingOnACompletedOlderReview(t *testing.T) {
	t.Parallel()
	for _, incremental := range []bool{false, true} {
		name := "full"
		if incremental {
			name = "incremental"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			reviewed := filedTask(t, ctx, store, "t1", repo)
			kindTag := "path-trigger-review:acme/widgets#1:code"
			review := func(id, head string, links ...model.Link) model.Task {
				t.Helper()
				r := filedTask(t, ctx, store, id, repo)
				r.Origin.Reason = model.ReasonReview
				r.Base = reviewed.BranchName()
				r.AutoMerge = true
				r.Tags = []string{model.PathTriggerMarkerTag, kindTag, "path-trigger-head:" + head}
				r.Links = append(r.Links, model.Link{Kind: model.LinkProposedBy, Target: reviewed.ID})
				r.Links = append(r.Links, links...)
				if err := store.PutTask(ctx, r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			old := review("r1", "08f6232f", model.Link{Kind: model.LinkFixes, Target: "acme/widgets#2"})
			done := baseTime
			if err := store.ObserveField(ctx, old.ID, done, func(o *model.Observation) {
				o.CompletedAt = &done
				o.MergeQueueState = "queued"
				o.MergeQueueStateAt = &done
			}); err != nil {
				t.Fatal(err)
			}
			newer := review("r2", "1234abcd", model.Link{Kind: model.LinkDependsOn, Target: old.ID})
			if incremental {
				newer.Tags = append(newer.Tags, "path-trigger-since:08f6232f")
				if err := store.PutTask(ctx, newer); err != nil {
					t.Fatal(err)
				}
			}
			if ready, err := store.IsReady(ctx, newer.ID); err != nil || ready {
				t.Fatalf("the re-review is ready (%v, %v) before the sweep; the fixture is not the stall", ready, err)
			}

			if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(time.Hour)); err != nil {
				t.Fatalf("SyncReviews: %v", err)
			}
			if state, err := store.State(ctx, old.ID); err != nil || state != model.StateCompleted {
				t.Errorf("the older review reads %q (%v), want it left completed with its fix to land", state, err)
			}
			gotNew, _ := store.GetTask(ctx, newer.ID)
			if hasLink(*gotNew, model.LinkDependsOn, old.ID) {
				t.Fatalf("the re-review still depends on the completed review: %+v", gotNew.Links)
			}
			if ready, err := store.IsReady(ctx, newer.ID); err != nil || !ready {
				t.Errorf("the re-review is not ready to dispatch (%v, %v)", ready, err)
			}
			stillIncremental := false
			for _, tag := range gotNew.Tags {
				stillIncremental = stillIncremental || strings.HasPrefix(tag, "path-trigger-since:")
			}
			if stillIncremental {
				t.Errorf("the re-review still reads only the delta past a fix that has not landed: %v", gotNew.Tags)
			}
			if bodies := commentBodies(t, ctx, store, newer.ID); len(bodies) == 0 ||
				!strings.Contains(bodies[len(bodies)-1], "No longer waiting on review r1") {
				t.Errorf("the re-review does not say why it stopped waiting: %q", bodies)
			}
		})
	}
}
