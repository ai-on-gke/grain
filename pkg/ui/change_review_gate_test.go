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

package ui_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// gatedCLs is a changelist provider that gates Submit on its required
// reviews having passed on the current snapshot.
type gatedCLs struct {
	fakeCLs
	cleared   bool
	submitted []string
}

func (g *gatedCLs) Kind() model.LinkKind { return model.LinkChangelist }
func (g *gatedCLs) ReviewsMissing(context.Context, model.Task, string) ([]string, error) {
	if g.cleared {
		return nil, nil
	}
	return []string{"security"}, nil
}
func (g *gatedCLs) GateSubmittable(context.Context, model.Task, string) (bool, string, error) {
	if g.cleared {
		return true, "", nil
	}
	return false, "the security review has not passed on snapshot 4", nil
}
func (g *gatedCLs) Submit(_ context.Context, _ model.Task, target string, by change.Person) (change.Result, error) {
	g.submitted = append(g.submitted, target+"|"+by.Name)
	return change.Result{Target: target, Message: "Submitted " + target + "."}, nil
}

// Submit on a CL waits for the provider's gate: refused, with its reason,
// while a required review is missing, and let through once it passes.
func TestSubmitOnACLWaitsForItsRequiredReviews(t *testing.T) {
	client, store, ctx := testClient(t)
	gate := &gatedCLs{}
	change.Register(gate)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.Forget("cl/93")
	})
	seedAwaitingSubmit(t, store, ctx, "93", "cl/93")
	obs := submittable
	obs.Target, obs.Snapshot = "cl/93", 4
	change.Remember(obs)

	if got, _ := client.GetTask(ctx, "93"); got.CLSubmit == nil || got.CLSubmit.CanSubmit ||
		!strings.Contains(strings.Join(got.CLSubmit.Reasons, "; "), "security review has not passed") {
		t.Fatalf("clSubmit = %+v, want disabled with the gate's reason", got.CLSubmit)
	}
	err := client.Submit(ctx, "93")
	var ve *ui.ValidationError
	if err == nil || !strings.Contains(err.Error(), "security review has not passed") {
		t.Fatalf("Submit = %v, want it refused with the gate's reason", err)
	}
	if !errors.As(err, &ve) {
		t.Errorf("Submit error %T, want a validation error the page can show", err)
	}
	if len(gate.submitted) != 0 {
		t.Fatalf("a refused Submit reached the provider: %v", gate.submitted)
	}

	gate.cleared = true
	if got, _ := client.GetTask(ctx, "93"); got.CLSubmit == nil || !got.CLSubmit.CanSubmit {
		t.Fatalf("clSubmit = %+v, want submittable once the review passed", got.CLSubmit)
	}
	if err := client.Submit(ctx, "93"); err != nil {
		t.Fatalf("Submit after the review passed: %v", err)
	}
	if task, _ := store.GetTask(ctx, "93"); len(gate.submitted) != 1 || len(change.LiveChanges(*task)) != 0 {
		t.Fatalf("Submit did not go through once the gate allowed it: submitted=%v links=%+v", gate.submitted, task.Links)
	}
}

// A CL also held for a person's approval is gated all the same: the
// approvals path is not reached until the review gate allows Submit.
func TestSubmitWithApprovalsOnACLWaitsForItsRequiredReviews(t *testing.T) {
	client, store, ctx := testClient(t)
	gate := &gatedCLs{}
	change.Register(gate)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.Forget("cl/94")
	})
	seedAwaitingSubmit(t, store, ctx, "94", "cl/94")
	if err := store.UpdateTask(ctx, "94", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.ApprovalRequiredLink("builtin:path-triggers", []string{"carol"}))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	obs := submittable
	obs.Target, obs.Snapshot = "cl/94", 4
	change.Remember(obs)
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "carol"})

	if err := client.Submit(person, "94"); err == nil || !strings.Contains(err.Error(), "security review has not passed") {
		t.Fatalf("Submit = %v, want it refused with the gate's reason", err)
	}
	task, _ := store.GetTask(ctx, "94")
	if len(gate.submitted) != 0 || len(model.PendingApprovals(*task)) != 1 {
		t.Fatalf("a refused Submit changed the hold: submitted=%v pending=%+v", gate.submitted, model.PendingApprovals(*task))
	}

	gate.cleared = true
	if err := client.Submit(person, "94"); err != nil {
		t.Fatalf("Submit after the review passed: %v", err)
	}
	if task, _ := store.GetTask(ctx, "94"); len(gate.submitted) != 1 || len(model.PendingApprovals(*task)) != 0 || len(change.LiveChanges(*task)) != 0 {
		t.Fatalf("Submit did not go through once the gate allowed it: submitted=%v pending=%+v links=%+v",
			gate.submitted, model.PendingApprovals(*task), task.Links)
	}
}

// An agent's live proposal may not ask for a review grant
// (model.ReviewOfArg): only grain files a task holding one.
func TestCreateTaskRefusesAProposalAskingForAReviewGrant(t *testing.T) {
	client, store, ctx := testClient(t)
	if err := store.PutTask(ctx, model.Task{ID: "95", Title: "parent", Intent: model.IntentImplement}); err != nil {
		t.Fatal(err)
	}
	_, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "review it myself", Description: "d", ProposedBy: "95",
		Capabilities: &[]string{`https://caps.example/workspace#workspace={"readonly":true,"review_of":"95","snapshot":1}`},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), model.ReviewOfArg) {
		t.Fatalf("CreateTask = %v, want a validation error naming %s", err, model.ReviewOfArg)
	}
}
