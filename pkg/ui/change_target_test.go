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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// testTargetKind is a change target kind registered by this file alone,
// standing in for one an edition registers (which core
// never imports): it attaches a grant and asks for a person when its
// spec names a workspace.
const testTargetKind = "ui-test-target"

func init() {
	model.RegisterChangeTarget(model.ChangeTargetRegistration{
		Kind: testTargetKind,
		EnsureGrants: func(_ json.RawMessage, grants []model.Grant) ([]model.Grant, error) {
			for _, g := range grants {
				if g.Capability == "test-workspace" {
					return grants, nil
				}
			}
			return append(grants, model.Grant{Capability: "test-workspace"}), nil
		},
		NeedsPersonApproval: func(spec json.RawMessage) (bool, string) {
			if strings.Contains(string(spec), "workspace") {
				return true, "it names another workspace"
			}
			return false, ""
		},
	})
}

// A change-target task is filed with no repo -- even where the deployment has a
// default target -- carrying its change target, which is stored.
func TestCreateTaskWithAChangeTargetFilesItWithNoRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix it in the workspace", Approved: true,
		ChangeTarget: &model.ChangeTarget{Kind: testTargetKind, Spec: json.RawMessage(`{"sync_to":"head"}`)},
	})
	if err != nil {
		t.Fatalf("filing a change-target task: %v", err)
	}
	if task.Repo != "" {
		t.Errorf("repo = %q, want none", task.Repo)
	}
	stored := mustGetTask(t, ctx, store, task.ID)
	if stored.Target != nil {
		t.Errorf("stored target = %+v, want nil", stored.Target)
	}
	if stored.ChangeTarget == nil || stored.ChangeTarget.Kind != testTargetKind ||
		stored.ChangeTarget.SpecString() != `{"sync_to":"head"}` {
		t.Errorf("stored change target = %+v", stored.ChangeTarget)
	}
}

// Every git-shaped field a change-target task cannot use is refused with a
// reason, rather than dropped.
func TestCreateTaskWithAChangeTargetRefusesGitOnlyFields(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	for name, c2 := range map[string]struct {
		req  ui.CreateTaskRequest
		want string
	}{
		"repo":      {ui.CreateTaskRequest{Repo: "acme/widgets"}, "repo"},
		"branch":    {ui.CreateTaskRequest{Base: "main"}, "branch"},
		"automerge": {ui.CreateTaskRequest{AutoMerge: true}, "auto_merge"},
		"repair":    {ui.CreateTaskRequest{FixesBaseBranch: true}, "fixes_base_branch"},
		"goal":      {ui.CreateTaskRequest{Goal: &model.GoalDecl{Kind: model.GoalBaseGreen}}, "base-green"},
		"writes":    {ui.CreateTaskRequest{Writes: []ui.WriteTarget{{Repo: "acme/widgets"}}}, "writes"},
		"kind":      {ui.CreateTaskRequest{ChangeTarget: &model.ChangeTarget{Kind: "perforce"}}, "unknown change target"},
	} {
		req := c2.req
		req.Title = "refused"
		if req.ChangeTarget == nil {
			req.ChangeTarget = &model.ChangeTarget{Kind: testTargetKind}
		}
		_, err := c.CreateTask(ctx, req)
		var ve *ui.ValidationError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), c2.want) {
			t.Errorf("%s: error = %v, want a ValidationError naming %q", name, err, c2.want)
		}
	}
}

// The kind's implied grant is attached at filing, and a target the kind
// says needs a person files a proposal for one whatever the proposer's
// permission would have done.
func TestProposedChangeTargetAttachesItsGrantAndWaitsForAPersonWhenItSaysSo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	parent, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "the whole job", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}

	own, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "own workspace", Description: "d", ProposedBy: parent.ID,
		ChangeTarget: &model.ChangeTarget{Kind: testTargetKind},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustGetTask(t, ctx, store, own.ID)
	if !slices.ContainsFunc(stored.Grants, func(g model.Grant) bool { return g.Capability == "test-workspace" }) {
		t.Errorf("grants = %+v, want the target's implied grant attached", stored.Grants)
	}

	other, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "someone else's workspace", Description: "d", ProposedBy: parent.ID, Approved: true,
		ChangeTarget: &model.ChangeTarget{Kind: testTargetKind, Spec: json.RawMessage(`{"workspace":"theirs"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.State != model.StateProposed {
		t.Errorf("state = %s, want proposed: naming another workspace is a person's to approve", other.State)
	}
}

// A template bound to a change target (`change-target: <kind>` in its settings
// file) files every task against it with no repo, and a template cannot
// be bound to a repo and a change target at once.
func TestTemplateChangeTargetFilesItsTasksAgainstIt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	c.Config.Capabilities = append(c.Config.Capabilities, ui.Capability{ID: "test-workspace", Name: "Test workspace"})
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "ws", Title: "fix it in the workspace", Description: "d",
		ChangeTarget: &model.ChangeTarget{Kind: testTargetKind, Spec: json.RawMessage(`{"sync_to":"head"}`)},
	})
	if err != nil {
		t.Fatalf("creating a change-target template: %v", err)
	}
	if tmpl.ChangeTarget == nil || tmpl.ChangeTarget.Kind != testTargetKind {
		t.Fatalf("template change target = %+v", tmpl.ChangeTarget)
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatalf("filing from the change-target template: %v", err)
	}
	stored := mustGetTask(t, ctx, store, task.ID)
	if stored.Target != nil || stored.ChangeTarget == nil || stored.ChangeTarget.SpecString() != `{"sync_to":"head"}` {
		t.Errorf("filed task target %+v, change target %+v", stored.Target, stored.ChangeTarget)
	}

	_, err = c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "both", Title: "t", Repo: "acme/widgets",
		ChangeTarget: &model.ChangeTarget{Kind: testTargetKind},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "repo") {
		t.Errorf("template with a repo and change target: error %v, want a ValidationError naming repo", err)
	}
}

// checkedTargetKind is a change target kind whose filing check is a
// control tool, standing in for a start_cl check.
const checkedTargetKind = "ui-test-checked-target"

func init() {
	model.RegisterChangeTarget(model.ChangeTargetRegistration{
		Kind: checkedTargetKind,
		FilingCheck: func(spec json.RawMessage) (string, map[string]any) {
			var s struct {
				CL int `json:"cl"`
			}
			if json.Unmarshal(spec, &s) != nil || s.CL == 0 {
				return "", nil
			}
			return "check_cl", map[string]any{"cl": s.CL}
		},
	})
}

// clChecker is a registry whose one server answers check_cl: CL 1 is
// fine, CL 2 is submitted, and CL 3 cannot be read.
type clChecker struct {
	calls []map[string]any
}

func (c *clChecker) Statuses() []servers.Status {
	return []servers.Status{
		{ID: "https://x.example/other", State: servers.Attached},
		{ID: "https://x.example/workspace", State: servers.Attached, Tools: []string{"check_cl"}},
	}
}

func (c *clChecker) CallTool(_ context.Context, id, tool string, args map[string]any, _ time.Duration) ([]byte, error) {
	if id != "https://x.example/workspace" || tool != "check_cl" {
		return nil, fmt.Errorf("capability server %s advertises no tool %s", id, tool)
	}
	c.calls = append(c.calls, args)
	switch args["cl"] {
	case 2:
		return []byte(`{"ok":false,"reason":"CL 2 is already submitted"}`), nil
	case 3:
		return nil, errors.New("the host is unreachable")
	}
	return []byte(`{"ok":true}`), nil
}

// A target with a filing check is checked live at filing by the server
// that offers the tool: refused with its reason, and filed when the
// server cannot answer, for dispatch to check again.
func TestCreateTaskChecksAChangeTargetLive(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	checker := &clChecker{}
	c.Config.CapabilityServers = checker
	file := func(cl int) error {
		_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "continue it", Approved: true,
			ChangeTarget: &model.ChangeTarget{Kind: checkedTargetKind, Spec: json.RawMessage(fmt.Sprintf(`{"cl":%d}`, cl))},
		})
		return err
	}
	if err := file(1); err != nil {
		t.Errorf("CL 1: %v", err)
	}
	var ve *ui.ValidationError
	if err := file(2); !errors.As(err, &ve) || !strings.Contains(err.Error(), "already submitted") {
		t.Errorf("CL 2: error = %v, want a ValidationError saying it is submitted", err)
	}
	if err := file(3); err != nil {
		t.Errorf("CL 3, unreachable host: %v, want it filed", err)
	}
	if len(checker.calls) != 3 {
		t.Errorf("check_cl called %d times, want 3: %v", len(checker.calls), checker.calls)
	}
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "no cl", Approved: true, ChangeTarget: &model.ChangeTarget{Kind: checkedTargetKind}}); err != nil || len(checker.calls) != 3 {
		t.Errorf("a target needing no check: %v, %d calls", err, len(checker.calls))
	}
}

// A change-target task with no live CL has nothing to Submit, and a
// change-target task cannot be turned to auto-merge by editing it: either
// would leave it holding auto-merge, which every dispatch refuses
// (task 1860).
func TestAChangeTargetTaskWithNoCLIsNeverTurnedToAutoMerge(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix it in the workspace", Approved: true,
		ChangeTarget: &model.ChangeTarget{Kind: testTargetKind},
	})
	if err != nil {
		t.Fatalf("filing a change-target task: %v", err)
	}

	err = c.Submit(ctx, task.ID)
	var verr *ui.ValidationError
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), "no "+testTargetKind+" change to submit yet") {
		t.Errorf("Submit = %v, want a validation error saying there is no change to submit yet", err)
	}
	on := true
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{AutoMerge: &on}); !errors.As(err, &verr) ||
		!strings.Contains(err.Error(), "auto_merge cannot be set") {
		t.Errorf("UpdateTask(autoMerge) = %v, want a validation error refusing auto_merge", err)
	}
	if stored := mustGetTask(t, ctx, store, task.ID); stored.AutoMerge {
		t.Error("the change-target task was left holding auto-merge")
	}
}
