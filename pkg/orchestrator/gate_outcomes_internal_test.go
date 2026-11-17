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

package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func observedOutcomeStatus(t *testing.T, store *model.Store, id, binding string, now time.Time) string {
	t.Helper()
	v, _, err := store.ObserveTaskGates(context.Background(), id, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range v.Bindings {
		if b.ID == binding {
			return v.Results[i].Result.Status
		}
	}
	t.Fatalf("missing %s", binding)
	return ""
}
func TestShadowIntegrationUsesActualPRAndCLEvents(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{ID: "shadow-webhook", Title: "PR outcome", Intent: model.IntentImplement, Target: &repo, Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#77"}}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }); err != nil {
		t.Fatal(err)
	}
	ev := ghwebhook.Event{Action: "closed", Number: 77, HeadSHA: "exact-webhook-head", Merged: true, SenderLogin: "outside-person"}
	if err := handlePullRequestEvent(ctx, Deps{Store: store}, repo, "", "", nil, ev, now); err != nil {
		t.Fatal(err)
	}
	if got := observedOutcomeStatus(t, store, task.ID, "pr_merged", now); got != "met" {
		t.Fatalf("webhook merge=%s", got)
	}
	v, _, _ := store.ObserveTaskGates(ctx, task.ID, now)
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), "outside-person") {
		t.Fatal("external merger attribution missing")
	}
	// A mismatching task/resource number cannot consume this merge receipt.
	now = now.Add(time.Second)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#78"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := observedOutcomeStatus(t, store, task.ID, "pr_merged", now); got != "unknown" {
		t.Fatalf("replacement PR reused old receipt: %s", got)
	}
	// The ordinary scoped provider poll records every CL independently.
	seedCLTask(t, ctx, store, "shadow-polled-cls", "cl/601", "cl/602")
	clTask, _ := store.GetTask(ctx, "shadow-polled-cls")
	clTask.ChangeTarget = &model.ChangeTarget{Kind: "changelist", Spec: json.RawMessage(`{"workspace":"test"}`)}
	if err := store.PutTask(ctx, *clTask); err != nil {
		t.Fatal(err)
	}
	p := &landingCLs{}
	for i, target := range []string{"cl/601", "cl/602"} {
		obs := change.Observation{Target: target, Snapshot: i + 1, Status: change.StatusSubmitted}
		if err := applyChangeObservation(ctx, store, p, *clTask, obs, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		clTask, _ = store.GetTask(ctx, clTask.ID)
	}
	if got := observedOutcomeStatus(t, store, clTask.ID, "cl_submitted", now.Add(time.Second)); got != "met" {
		t.Fatalf("all polled CLs=%s", got)
	}
	// Exact snapshot absence stays unknown even when legacy submission closes.
	seedCLTask(t, ctx, store, "shadow-unpinned-cl", "cl/603")
	unpinned, _ := store.GetTask(ctx, "shadow-unpinned-cl")
	if err := change.RecordSubmitted(ctx, store, *unpinned, "cl/603", "cl/1603", "", true, now, change.Observation{Target: "cl/603", Status: change.StatusSubmitted}); err != nil {
		t.Fatal(err)
	}
	if got := observedOutcomeStatus(t, store, unpinned.ID, "cl_submitted", now); got != "unknown" {
		t.Fatalf("unversioned submitted snapshot=%s", got)
	}
}
func TestShadowPositiveLandedEquivalenceRequiresPins(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	for _, pinned := range []bool{false, true} {
		id := "shadow-equivalence-unpinned"
		if pinned {
			id = "shadow-equivalence-pinned"
		}
		task := model.Task{ID: id, Title: "Landed work", Intent: model.IntentImplement, Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		fresh, err := store.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		task = *fresh
		w := landedWork{Known: true, Landed: []string{"fixed.go"}, ObservedHead: "head-1", ObservedBase: "base-2", Pinned: pinned}
		if err := finishAlreadyLanded(ctx, store, task, &alreadyLandedError{Work: w}, now); err != nil {
			t.Fatal(err)
		}
		if pinned {
			if got := observedOutcomeStatus(t, store, id, "pr_merged", now); got != "met" {
				t.Fatalf("positive equivalence=%s", got)
			}
		} else {
			v, _, err := store.ObserveTaskGates(ctx, id, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range v.Results {
				if r.Result.Type == "integration" && r.Result.Status == "met" {
					t.Fatal("unpinned landed classification passed")
				}
			}
		}
	}
	// The domain's ordinary detail read also records a closed-unmerged outcome.
	task := model.Task{ID: "shadow-closed-pr", Title: "closed", Intent: model.IntentImplement, Target: &model.RepoRef{Owner: "acme", Name: "widgets"}, Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#8"}}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	ref := *model.TaskPullRequest(task)
	if err := recordPullRequestEvents(ctx, store, task.ID, ref, nil, github.PullRequestDetail{Number: 8, HeadSHA: "closed-head", State: "closed"}, model.PrClosed, now); err != nil {
		t.Fatal(err)
	}
	if got := observedOutcomeStatus(t, store, task.ID, "pr_merged", now); got != "unmet" {
		t.Fatalf("closed-unmerged=%s", got)
	}
}
