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
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A change held for named approvers (grain/task-1714) is released only by
// a person's Submit. Every person is the deployment's owner -- whoever
// holds the access code is -- so one person's Submit approves every rule,
// whatever it names, while an agent's approves none.
func TestSubmitOnASensitiveHoldWaitsForAPerson(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = false
		tk.Links = append(tk.Links,
			model.ApprovalRequiredLink("trigger:payments", []string{"team:default", "alice"}),
			model.ApprovalRequiredLink("builtin:path-triggers", []string{"carol"}))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stored := func() model.Task {
		got, err := store.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatal(err)
		}
		return *got
	}

	var forbidden *ui.ForbiddenError
	agent := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "run-1"})
	err := c.Submit(agent, task.ID)
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "a person holding the access code") {
		t.Fatalf("an agent's Submit: %v, want a refusal naming who can", err)
	}
	if got := stored(); got.AutoMerge || len(model.PendingApprovals(got)) != 2 {
		t.Fatalf("a refused Submit changed the hold: automerge=%v pending=%+v", got.AutoMerge, model.PendingApprovals(got))
	}

	if err := c.Submit(ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "mallory"}), task.ID); err != nil {
		t.Fatalf("a person's Submit: %v", err)
	}
	got := stored()
	if !got.AutoMerge || len(model.PendingApprovals(got)) != 0 {
		t.Fatalf("every rule approved: automerge=%v pending=%+v", got.AutoMerge, model.PendingApprovals(got))
	}
	view, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ui.ApprovalView{}
	for _, a := range view.Approvals {
		by[a.Rule] = a
	}
	if len(by) != 2 || by["trigger:payments"].ApprovedBy != "mallory" || by["builtin:path-triggers"].ApprovedBy != "mallory" ||
		by["trigger:payments"].Source != ".grain/triggers/payments.yml" || by["builtin:path-triggers"].Source != "built in" {
		t.Errorf("approvals view = %+v", view.Approvals)
	}
}
