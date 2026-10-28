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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

// externalSource is an external request a human approved to auto-merge:
// the task whose agent, port, review or repair files the derived work
// GSA1-02 is about.
func externalSource(id string, tags ...string) model.Task {
	return model.Task{
		ID: id, Title: "External work", Target: &widgets, Base: "main",
		Creator: "friend@example.com", AutoMerge: true, Tags: tags,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}},
	}
}

// requireInherited fails unless derived names source's requester and
// carries no human approval -- so local policy holds it for one of its own.
func requireInherited(t *testing.T, what string, derived, source model.Task) {
	t.Helper()
	if derived.Creator != source.Creator {
		t.Errorf("%s: Creator = %q, want its source's %q", what, derived.Creator, source.Creator)
	}
	if a := derived.Approval; a != nil && a.Actor.Kind == model.PrincipalHuman {
		t.Errorf("%s: carries a human approval (%s) it did not get for itself", what, a.Actor.ID)
	}
	p, err := policy.Parse([]byte("repos: [acme/*]\nlocal_requesters: [owner@example.com]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Check(derived, model.NetworkOpen); err == nil || !strings.Contains(err.Error(), "no human approval") {
		t.Errorf("%s: local policy err = %v, want it held for a fresh approval", what, err)
	}
}

func TestPortsInheritTheirSourcesRequester(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	for _, source := range []model.Task{
		externalSource("7"),
	} {
		ref := model.PullRequestRef{Repo: widgets, Number: 30}
		port, err := newPortTask(ctx, store, source, ref, github.PullRequestDetail{Title: "x", BaseRef: "main"}, "candidate", now)
		if err != nil {
			t.Fatal(err)
		}
		requireInherited(t, "port of "+source.ID, port, source)
	}
}

func TestStackedReviewsInheritTheReviewedTasksRequester(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	source := externalSource("7")
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	review, err := putStackedReview(ctx, store, source, model.Template{ID: "rev", Title: "Review", Body: "Read it."},
		"Review 7", "Read it.", now)
	if err != nil {
		t.Fatal(err)
	}
	requireInherited(t, "review", review, source)
	stored, err := store.GetTask(ctx, review.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the review back: %v", err)
	}
	requireInherited(t, "stored review", *stored, source)
}

func TestEndOfRunProposalsInheritTheProposersRequester(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	source := externalSource("7")
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{{
		Name:      "propose_task",
		Arguments: map[string]any{"title": "Follow up", "body": "And then this.", "auto_merge": true},
	}}}
	if err := relayProposedTasks(ctx, store, nil, source, result, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range tasks {
		if task.Parent() == source.ID {
			found = true
			requireInherited(t, "end-of-run proposal", task, source)
		}
	}
	if !found {
		t.Fatal("no proposal was filed")
	}
}

// When relayProposedTasks replays a propose_task call that names an
// interactive template_id, the resulting proposal carries the template's
// Interactive flag while remaining unapproved, matching ui.Client.CreateTask.
func TestRelayProposedTasksCarriesInteractiveFromTemplate(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	tmpl := model.Template{
		ID:          "template-interactive",
		Name:        "Interactive triage",
		Title:       "Triage incident interactively",
		Body:        "Chat with the operator while triaging.",
		Interactive: true,
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	source := externalSource("7")
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{{
		Name:      "propose_task",
		Arguments: map[string]any{"template_id": tmpl.ID},
	}}}
	if err := relayProposedTasks(ctx, store, nil, source, result, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var filed *model.Task
	for i := range tasks {
		if tasks[i].Parent() == source.ID {
			filed = &tasks[i]
			break
		}
	}
	if filed == nil {
		t.Fatal("relayProposedTasks did not file a proposal")
	}
	if !filed.Interactive {
		t.Error("proposal filed from interactive template is not interactive")
	}
	if filed.Approval != nil {
		t.Errorf("proposal Approval = %+v, want nil (unapproved)", filed.Approval)
	}
}

func TestBaseBranchRepairsInheritTheMergedTasksRequester(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	source := externalSource("7")
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	row := model.BaseBranchCI{Repo: widgets, Branch: "main", Commit: "abc1234def", Detail: "go failed", MergedTaskID: source.ID}
	if err := fileBaseBranchRepair(ctx, Deps{Store: store}, row, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID != source.ID {
			requireInherited(t, "repair", task, source)
			return
		}
	}
	t.Fatal("no repair was filed")
}

// A task filed before derived work inherited its requester -- or by a
// path that names none -- is judged at dispatch as its root's, found by
// walking the chain it was derived from: never as grain's own for
// naming nobody.
func TestLocalPolicyJudgesADerivedTaskAsItsRootRequesters(t *testing.T) {
	store, ctx := openTreeStore(t)
	p, err := policy.Parse([]byte("repos: [acme/widgets]\nlocal_requesters: [owner@example.com]\n"))
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: store, Policy: p}
	autoSubmitted := &model.Attribution{Actor: model.AutoSubmitPrincipal}
	put := func(task model.Task) {
		t.Helper()
		if task.Target == nil {
			task.Target = &widgets
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	proposedBy := func(id string) []model.Link { return []model.Link{{Kind: model.LinkProposedBy, Target: id}} }

	put(externalSource("1"))
	put(model.Task{ID: "2", Links: proposedBy("1"), Approval: autoSubmitted})
	put(model.Task{ID: "3", Links: []model.Link{{Kind: model.LinkPortOf, Target: "2"}}, Approval: autoSubmitted})
	put(model.Task{ID: "4", Title: "scheduled"})
	put(model.Task{ID: "5", Links: proposedBy("4"), Approval: autoSubmitted})
	put(model.Task{ID: "6", Creator: "colleague@example.com",
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}}})
	put(model.Task{ID: "7", Links: proposedBy("6"), Approval: autoSubmitted})
	put(model.Task{ID: "8", Links: proposedBy("missing"), Approval: autoSubmitted})
	put(model.Task{ID: "9", Creator: "owner@example.com"})
	put(model.Task{ID: "10", Links: proposedBy("9"), Approval: autoSubmitted})

	held := heldByPolicy(ctx, deps)
	for id, want := range map[string]string{
		"2": "friend@example.com (through task 1)",
		"3": "friend@example.com (through task 2)",
		"7": "colleague@example.com (through task 6)",
		"8": "an unknown requester (through task missing)",
	} {
		if !held(id) {
			t.Errorf("task %s was dispatched", id)
			continue
		}
		wait, err := store.GrantWaitOf(ctx, id)
		if err != nil || wait == nil || !strings.Contains(wait.Resource, want) || !strings.Contains(wait.Resource, "no human approval") {
			t.Errorf("task %s's wait = %+v, %v; want one naming %q", id, wait, err, want)
		}
	}
	for _, id := range []string{"4", "5", "10"} {
		if held(id) {
			wait, _ := store.GrantWaitOf(ctx, id)
			t.Errorf("task %s, whose root is this machine's own, was held: %+v", id, wait)
		}
	}

	// A human's approval of the derived task itself is what lets it run.
	task, _ := store.GetTask(ctx, "2")
	task.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}}
	put(*task)
	if held("2") {
		wait, _ := store.GrantWaitOf(ctx, "2")
		t.Errorf("a derived task a human approved was held: %+v", wait)
	}
}

func TestRootRequesterStopsOnACycle(t *testing.T) {
	store, ctx := openTreeStore(t)
	for _, task := range []model.Task{
		{ID: "1", Target: &widgets, Links: []model.Link{{Kind: model.LinkPortOf, Target: "2"}}},
		{ID: "2", Target: &widgets, Links: []model.Link{{Kind: model.LinkPortOf, Target: "1"}}},
	} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	task, _ := store.GetTask(ctx, "1")
	r, err := rootRequester(context.Background(), store, *task)
	if err != nil || !r.Unresolved {
		t.Errorf("rootRequester on a cycle = %+v, %v; want unresolved", r, err)
	}
}
