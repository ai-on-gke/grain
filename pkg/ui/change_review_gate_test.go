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
	cleared bool
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

// Submit on a CL waits for the provider's gate: refused, with its reason,
// while a required review is missing, and let through once it passes.
func TestSubmitOnACLWaitsForItsRequiredReviews(t *testing.T) {
	client, store, ctx := testClient(t)
	gate := &gatedCLs{}
	change.Register(gate)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })
	if err := store.PutTask(ctx, model.Task{ID: "93", Title: "t", Intent: model.IntentImplement,
		Links: []model.Link{{Kind: model.LinkChangelist, Target: "cl/93"}}}); err != nil {
		t.Fatal(err)
	}

	err := client.Submit(ctx, "93")
	var ve *ui.ValidationError
	if err == nil || !strings.Contains(err.Error(), "security review has not passed") {
		t.Fatalf("Submit = %v, want it refused with the gate's reason", err)
	}
	if !errors.As(err, &ve) {
		t.Errorf("Submit error %T, want a validation error the page can show", err)
	}
	if task, _ := store.GetTask(ctx, "93"); task.AutoMerge {
		t.Fatal("a refused Submit turned auto-merge on")
	}

	gate.cleared = true
	if err := client.Submit(ctx, "93"); err != nil {
		t.Fatalf("Submit after the review passed: %v", err)
	}
	if task, _ := store.GetTask(ctx, "93"); !task.AutoMerge {
		t.Fatal("Submit did not go through once the gate allowed it")
	}
}

// A CL also held for a person's approval is gated all the same: the
// approvals path turns auto-merge on by itself, so the review gate is
// asked first.
func TestSubmitWithApprovalsOnACLWaitsForItsRequiredReviews(t *testing.T) {
	client, store, ctx := testClient(t)
	gate := &gatedCLs{}
	change.Register(gate)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })
	if err := store.PutTask(ctx, model.Task{ID: "94", Title: "t", Intent: model.IntentImplement,
		Links: []model.Link{{Kind: model.LinkChangelist, Target: "cl/94"},
			model.ApprovalRequiredLink("builtin:path-triggers", []string{"carol"})}}); err != nil {
		t.Fatal(err)
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "carol"})

	if err := client.Submit(person, "94"); err == nil || !strings.Contains(err.Error(), "security review has not passed") {
		t.Fatalf("Submit = %v, want it refused with the gate's reason", err)
	}
	task, _ := store.GetTask(ctx, "94")
	if task.AutoMerge || len(model.PendingApprovals(*task)) != 1 {
		t.Fatalf("a refused Submit changed the hold: automerge=%v pending=%+v", task.AutoMerge, model.PendingApprovals(*task))
	}

	gate.cleared = true
	if err := client.Submit(person, "94"); err != nil {
		t.Fatalf("Submit after the review passed: %v", err)
	}
	if task, _ := store.GetTask(ctx, "94"); !task.AutoMerge {
		t.Fatal("Submit did not go through once the gate allowed it")
	}
}
