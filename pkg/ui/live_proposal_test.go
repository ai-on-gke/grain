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
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A proposal filed while its run is still going is judged by the same
// gate as one replayed at the end of it
// (orchestrator.relayProposedTasks): the permission belongs to the task
// that proposed it, so a proposal from an auto-merge job landing where
// that job was allowed to land work is queued outright, and one landing
// anywhere else waits for a person however the request was phrased.
func TestProposalFromAnAutoMergeTaskIsQueuedWithinItsParentsPermission(t *testing.T) {
	client, _, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	piece, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a piece of it", ProposedBy: parent.ID, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if piece.State != model.StateQueued {
		t.Errorf("state = %s, want queued -- its parent's permission covers it", piece.State)
	}
	if !piece.AutoMerge {
		t.Error("autoMerge = false on a proposal its parent could have merged unreviewed itself")
	}

	// The same call from the same parent, landing on a branch that
	// parent was told nothing about: neither half of the permission
	// travels, and the request asking for auto-merge does not make it.
	elsewhere, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "on a release branch", ProposedBy: parent.ID, AutoMerge: true, Base: "release/2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere.State != model.StateProposed {
		t.Errorf("state = %s, want proposed -- nobody granted anything about that branch", elsewhere.State)
	}
	if elsewhere.AutoMerge {
		t.Error("autoMerge = true on a proposal landing where its parent was granted nothing")
	}
}

func TestProposalFromAutoApproveProposalsTaskIsQueuedWithoutAutoMerge(t *testing.T) {
	client, _, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "audit runner", Approved: true, AutoMerge: false, AutoApproveProposals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	piece, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a piece of it", ProposedBy: parent.ID, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if piece.State != model.StateQueued {
		t.Errorf("state = %s, want queued -- its parent has auto-approve-proposals", piece.State)
	}
	if piece.AutoMerge {
		t.Error("autoMerge = true, want false -- parent task does not auto-merge")
	}
}

// And a task nobody trusted with an unreviewed merge passes on neither
// half of it, which is the ordinary case and the one that keeps
// propose_task's contract intact for every deployment that never turns
// auto-merge on.
func TestInteractiveProposalIsImmediatelyVisibleAndIdempotent(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "director", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	req := ui.CreateTaskRequest{
		Title: "follow-up", Description: "do it", ProposedBy: parent.ID,
		ProposalKey: "proposal:" + parent.ID + ":stable",
	}
	first, err := client.CreateTask(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != model.StateProposed {
		t.Fatalf("state = %s", first.State)
	}
	again, err := client.CreateTask(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("retry id = %s, want %s", again.ID, first.ID)
	}
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, task := range tasks {
		if task.ID == first.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("visible proposal count = %d", count)
	}
	stored, err := store.GetTask(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, link := range stored.Links {
		if link.Kind == model.LinkProposedBy && link.Target == parent.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("proposal has no proposed-by provenance")
	}
}

// Capabilities do not travel from a task to the work it proposes, which
// docs/data-model.md's "sub-tasks are tasks" section calls the
// load-bearing rule: a proposal is filed with this deployment's defaults
// and asks for anything more on its own task, where a human can see the
// request.
//
// Enforced against the request rather than against the one caller that
// makes it. cmd/grain's daemonProposalFiler used to send its parent's
// whole capability list here, so the same propose_task call was filed
// with different authority depending on whether its run had a daemon to
// file through or was replayed at the end of the run
// (orchestrator.relayProposedTasks). A request that names ProposedBy now
// gets the defaults whatever its capabilities field says, so the two
// filing paths cannot drift apart again.
func TestProposalIsFiledWithDefaultCapabilitiesNotItsParents(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gcp-key")
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true,
		Capabilities: &[]string{"gcp-key", "self-repair"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// An unadorned proposal gets deployment/repo defaults, not its parent's grant set.
	piece, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a piece of it", ProposedBy: parent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(piece.Capabilities, []string{"gcp-key"}) {
		t.Errorf("capabilities = %v, want [gcp-key] -- the defaults, not the parent's grant set",
			piece.Capabilities)
	}
	stored, err := store.GetTask(ctx, piece.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range stored.Grants {
		if grant.Via != model.GrantByDefault {
			t.Errorf("%s recorded as %q, want %q: default capabilities record GrantByDefault",
				grant.Capability, grant.Via, model.GrantByDefault)
		}
	}

	// A proposal requesting capabilities receives defaults + requested capabilities.
	custom, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "custom proposal", ProposedBy: parent.ID,
		Capabilities: &[]string{"self-repair"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(custom.Capabilities, "gcp-key") || !slices.Contains(custom.Capabilities, "self-repair") {
		t.Errorf("custom capabilities = %v, want [gcp-key self-repair]", custom.Capabilities)
	}
}

func TestAutoSubmittedProposalInheritanceAndCapabilities(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gcp-key")
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "trusted to merge unread", Approved: true, AutoMerge: true,
		Capabilities: &[]string{"gcp-key", "self-repair"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Proposal with only defaults is a subset of parent grants and auto-merges -> Queued.
	piece, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a piece of it", ProposedBy: parent.ID, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if piece.State != model.StateQueued {
		t.Errorf("state = %s, want queued -- its parent's permission covers the defaults", piece.State)
	}

	// Proposal asking for capability NOT granted to parent (e.g. self-upgrade) cannot auto-submit -> Proposed.
	escalated, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "escalated proposal", ProposedBy: parent.ID, AutoMerge: true,
		Capabilities: &[]string{"self-upgrade"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if escalated.State != model.StateProposed {
		t.Errorf("state = %s, want proposed -- requested capabilities exceed parent grants", escalated.State)
	}
	if !strings.Contains(escalated.Description, "`self-upgrade`") {
		t.Errorf("body = %q, want a note naming self-upgrade -- approving the work must not "+
			"silently approve the capability", escalated.Description)
	}
	if strings.Contains(piece.Description, "grain: this proposal asked for") {
		t.Errorf("body = %q: a proposal asking for nothing new carries an escalation note", piece.Description)
	}
}

// grain#2409: a standing session trusted to queue its own work is not
// trusted to hand that work capabilities it does not hold. The proposal
// is still filed holding what it asked for, but for a person to approve.
func TestSessionProposalAskingForACapabilityItsSessionLacksWaitsForAPerson(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: model.DirectorSessionID, Title: "Director", TasksApproved: true},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Director", Interactive: true, Capabilities: &[]string{"self-debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustGetTask(t, ctx, store, session.ID)
	stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
	if err := store.PutTask(ctx, stored); err != nil {
		t.Fatal(err)
	}

	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs what the session has", Description: "d",
		ProposedBy: session.ID, Capabilities: &[]string{"self-debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.State != model.StateQueued {
		t.Errorf("state = %s, want queued -- the session holds self-debug itself", held.State)
	}

	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs self-repair", Description: "d",
		ProposedBy: session.ID, Capabilities: &[]string{"self-repair"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filed.State != model.StateProposed {
		t.Errorf("state = %s, want proposed -- the session never held self-repair", filed.State)
	}
	if !slices.Contains(filed.Capabilities, "self-repair") {
		t.Errorf("capabilities = %v, want self-repair kept for the person approving it", filed.Capabilities)
	}
	if proposal := mustGetTask(t, ctx, store, filed.ID); proposal.Approval != nil {
		t.Errorf("approved by %+v, want nobody", proposal.Approval)
	}
	if !strings.Contains(filed.Description, "`self-repair`") {
		t.Errorf("body = %q, want a note naming self-repair", filed.Description)
	}
}

func TestSessionProposalIsQueuedWhenTheDeploymentSaysThatSessionMay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approved bool
		want     model.State
		approver model.Principal
	}{
		{"the shipped default", false, model.StateProposed, model.Principal{}},
		{"a deployment that trusts its director", true, model.StateQueued, model.ConsoleSessionPrincipal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store, ctx := testClient(t)
			cfg := model.DefaultConfig()
			cfg.ConsoleSessions = []model.ConsoleSession{
				{ID: model.DirectorSessionID, Title: "Director", TasksApproved: tc.approved},
			}
			if err := store.PutConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			// The task holding the session's conversation: interactive,
			// approved with nobody having approved it, tagged with the
			// session id, and -- the point -- holding no auto-merge, so
			// nothing about AutoSubmits can account for the answer.
			session, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "Director", Interactive: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			stored := mustGetTask(t, ctx, store, session.ID)
			stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
			if err := store.PutTask(ctx, stored); err != nil {
				t.Fatal(err)
			}

			filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "let workflow jobs use the CI cache", Description: "as we just discussed",
				ProposedBy: session.ID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != tc.want {
				t.Errorf("state = %s, want %s -- TasksApproved is %v", filed.State, tc.want, tc.approved)
			}
			proposal := mustGetTask(t, ctx, store, filed.ID)
			if tc.approved {
				if proposal.Approval == nil || proposal.Approval.Actor != tc.approver {
					t.Errorf("approved by %+v, want %+v: who queued it has to stay answerable",
						proposal.Approval, tc.approver)
				}
				if !model.SelfApproved(proposal) {
					t.Error("a session-queued task is not counted as a level of self-approved " +
						"work, so nothing below it is bounded")
				}
			} else if proposal.Approval != nil {
				t.Errorf("approved by %+v, want nobody", proposal.Approval)
			}
			if proposal.AutoMerge {
				t.Error("autoMerge = true: a session that may queue work was granted nothing " +
					"about anybody's branch")
			}
			// And the record says who filed it. A proposal attributed to
			// the operator is a task nobody can tell from one a person
			// typed in, which is exactly how #308 read.
			if proposal.Origin.Attribution.Actor.Kind == model.PrincipalHuman {
				t.Errorf("filed by %+v -- a proposal read back as a human's own filing",
					proposal.Origin.Attribution.Actor)
			}
			behalf := proposal.Origin.Attribution.OnBehalfOf
			if behalf == nil || behalf.ID != session.ID {
				t.Errorf("filed on behalf of %+v, want the session that proposed it (%s)",
					behalf, session.ID)
			}
		})
	}
}

func mustGetTask(t *testing.T, ctx context.Context, store *model.Store, id string) model.Task {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatalf("no task %s", id)
	}
	return *task
}

func TestProposalWithReadsWritesAndReviewTemplate(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)

	err := store.PutTemplate(ctx, model.Template{
		ID:    "code-review-v1",
		Name:  "Code Review v1",
		Title: "Review: {{title}}",
		Body:  "Perform pre-merge code review.",
	})
	if err != nil {
		t.Fatal(err)
	}

	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "parent task", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := ui.CreateTaskRequest{
		Title:            "cross-repo child",
		Description:      "work across repos",
		ProposedBy:       parent.ID,
		ProposalKey:      "proposal:" + parent.ID + ":cross-repo",
		Reads:            []string{"bwsalmon/grain"},
		Writes:           []ui.WriteTarget{{Repo: "bwsalmon/grain", Base: "main"}},
		ReviewTemplateID: "code-review-v1",
	}
	child, err := client.CreateTask(ctx, req)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	stored := mustGetTask(t, ctx, store, child.ID)
	if stored.ReviewTemplateID != "code-review-v1" {
		t.Errorf("ReviewTemplateID = %q, want code-review-v1", stored.ReviewTemplateID)
	}
	grainRepo, err := model.ParseRepo("bwsalmon/grain")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Reads) != 1 || !stored.Reads[0].Same(grainRepo) {
		t.Errorf("Reads = %+v, want [bwsalmon/grain]", stored.Reads)
	}
	if len(stored.Writes) != 1 || !stored.Writes[0].Repo.Same(grainRepo) || stored.Writes[0].Base != "main" {
		t.Errorf("Writes = %+v, want [{bwsalmon/grain main}]", stored.Writes)
	}

	badReq := ui.CreateTaskRequest{
		Title:            "bad review template",
		ProposedBy:       parent.ID,
		ProposalKey:      "proposal:" + parent.ID + ":bad-tmpl",
		ReviewTemplateID: "nonexistent-template",
	}
	if _, err := client.CreateTask(ctx, badReq); err == nil {
		t.Fatal("expected error for nonexistent review template, got nil")
	}
}

// The daemon's propose_task filer holds a proposal's review template to
// its proposer's grants as relayProposedTasks does: one carrying a grant
// the parent lacks is replaced by no review (there is no default here),
// with a note saying why.
func TestProposalReviewTemplateBeyondTheParentsGrantsIsRefused(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	if err := store.PutTemplate(ctx, model.Template{ID: "ops-review", Name: "ops", Title: "ops",
		Grants: []model.Grant{{Capability: "api:deployment:upgrade", Via: model.GrantByLabel}}}); err != nil {
		t.Fatal(err)
	}
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "parent task", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "child", Description: "work", ProposedBy: parent.ID,
		ProposalKey: "proposal:" + parent.ID + ":ops", ReviewTemplateID: "ops-review",
	})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	stored := mustGetTask(t, ctx, store, child.ID)
	if stored.ReviewTemplateID != "" {
		t.Errorf("ReviewTemplateID = %q, want the refused template dropped", stored.ReviewTemplateID)
	}
	if !strings.Contains(stored.Body, "ops-review") || !strings.Contains(stored.Body, "api:deployment:upgrade") {
		t.Errorf("body does not say why the review template was refused: %q", stored.Body)
	}
}

// The bound on how far that permission propagates is applied here too
// (model.AutoSubmitLimits), because a proposal can be filed either way
// and a bound one path enforces is not a bound. A parent at the depth
// limit -- itself auto-submitted below a task that was auto-submitted
// too -- passes on its auto-merge and not its approval, and the
// proposal says why it is waiting.
func TestLiveProposalPastTheAutoSubmitDepthWaitsForAPerson(t *testing.T) {
	client, store, ctx := testClient(t)
	chain, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two levels of self-approved work below the person who approved
	// that one, which is what the default depth allows.
	for range 2 {
		next, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "a piece of it", ProposedBy: chain.ID, AutoMerge: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if next.State != model.StateQueued {
			t.Fatalf("state = %s at depth within the limit, want queued", next.State)
		}
		chain = next
	}

	past, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "and another", Description: "more of the same", ProposedBy: chain.ID, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if past.State != model.StateProposed {
		t.Errorf("state = %s past the depth limit, want proposed", past.State)
	}
	if !past.AutoMerge {
		t.Error("autoMerge = false -- the bound is on the approval, not on the merge queue")
	}
	stored, err := store.GetTask(ctx, past.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Body, "levels of self-approved work") {
		t.Errorf("the proposal's body does not say why it is waiting:\n%s", stored.Body)
	}
}

func TestProposalCarriesSandboxShapeAndNetworkAccess(t *testing.T) {
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "parent", Approved: true})
	if err != nil {
		t.Fatal(err)
	}

	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:           "heavy proposal",
		ProposedBy:      parent.ID,
		SandboxCPUs:     8,
		SandboxMemoryMB: 16384,
		SandboxDiskGB:   50,
		NetworkAccess:   string(model.NetworkNone),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if filed.SandboxCPUs != 8 || filed.SandboxMemoryMB != 16384 || filed.SandboxDiskGB != 50 {
		t.Errorf("filed sandbox shape = (%d, %d, %d), want (8, 16384, 50)",
			filed.SandboxCPUs, filed.SandboxMemoryMB, filed.SandboxDiskGB)
	}
	if filed.NetworkAccess != string(model.NetworkNone) {
		t.Errorf("filed networkAccess = %q, want %q", filed.NetworkAccess, model.NetworkNone)
	}

	stored := mustGetTask(t, ctx, store, filed.ID)
	if stored.SandboxCPUs != 8 || stored.SandboxMemoryMB != 16384 || stored.SandboxDiskGB != 50 {
		t.Errorf("stored sandbox shape = (%d, %d, %d), want (8, 16384, 50)",
			stored.SandboxCPUs, stored.SandboxMemoryMB, stored.SandboxDiskGB)
	}
	if stored.NetworkAccess != model.NetworkNone {
		t.Errorf("stored networkAccess = %q, want %q", stored.NetworkAccess, model.NetworkNone)
	}
}

func TestProposalRefusesInvalidSandboxShapeAndNetworkAccess(t *testing.T) {
	client, _, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "parent", Approved: true})
	if err != nil {
		t.Fatal(err)
	}

	// Invalid CPU shape (< 0)
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "bad shape",
		ProposedBy:  parent.ID,
		SandboxCPUs: -1,
	})
	if err == nil {
		t.Error("CreateTask with negative CPUs succeeded, want validation error")
	}

	// Invalid network access word
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:         "bad network",
		ProposedBy:    parent.ID,
		NetworkAccess: "invalid_net",
	})
	if err == nil {
		t.Error("CreateTask with invalid networkAccess succeeded, want validation error")
	}
}

func TestProposalRefusesSandboxShapeAndNetworkAccessWhenBackendIgnored(t *testing.T) {
	client, _, ctx := testClient(t)
	client.Config.SandboxShapeIgnored = true
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "parent", Approved: true})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "ignored shape",
		ProposedBy:  parent.ID,
		SandboxCPUs: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "no shape of their own") {
		t.Errorf("CreateTask with shape on ignored backend error = %v, want refuseIgnoredSandboxShape error", err)
	}

	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:         "ignored network",
		ProposedBy:    parent.ID,
		NetworkAccess: string(model.NetworkNone),
	})
	if err == nil || !strings.Contains(err.Error(), "networkAccess") {
		t.Errorf("CreateTask with none network on ignored backend error = %v, want refuseIgnoredNetworkAccess error", err)
	}
}

func TestProposalWithNoRepo(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "director", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	piece, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "cluster analysis", ProposedBy: parent.ID, NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if piece.Repo != "" {
		t.Errorf("repo = %q, want empty for no_repo task", piece.Repo)
	}
	stored, err := store.GetTask(ctx, piece.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Target != nil {
		t.Errorf("stored target = %v, want nil", stored.Target)
	}
}

func TestProposalWithAtFront(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "director", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "task 1", Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "task 2", Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	atFront := true
	urgent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "urgent blocker", ProposedBy: parent.ID, AtFront: &atFront, Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	storedT1, _ := store.GetTask(ctx, t1.ID)
	storedT2, _ := store.GetTask(ctx, t2.ID)
	storedUrgent, _ := store.GetTask(ctx, urgent.ID)
	if storedUrgent.OrderKey >= storedT1.OrderKey || storedUrgent.OrderKey >= storedT2.OrderKey {
		t.Errorf("urgent orderKey %v should be before t1 %v and t2 %v", storedUrgent.OrderKey, storedT1.OrderKey, storedT2.OrderKey)
	}
}

func TestProposalFromTemplate(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "director", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	tmplID := "tmpl-investigation"
	if err := store.PutTemplate(ctx, model.Template{
		ID:    tmplID,
		Name:  "Investigation",
		Title: "Investigate {{component}}",
		Body:  "Check logs for {{component}} in {{env}}",
		Fields: []model.TemplateField{
			{Name: "component", Required: true},
			{Name: "env", Default: "prod"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Missing required field
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		ProposedBy: parent.ID,
		TemplateID: tmplID,
		Fields:     map[string]string{"env": "staging"},
	})
	if err == nil {
		t.Fatal("expected error for missing required template field")
	}

	// Non-existent template
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		ProposedBy: parent.ID,
		TemplateID: "non-existent-tmpl",
	})
	if err == nil {
		t.Fatal("expected error for non-existent template")
	}

	// Successful instantiation
	inst, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		ProposedBy: parent.ID,
		TemplateID: tmplID,
		Fields:     map[string]string{"component": "auth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Title != "Investigate auth" {
		t.Errorf("title = %q, want %q", inst.Title, "Investigate auth")
	}
	stored, err := store.GetTask(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Body, "Check logs for auth in prod") {
		t.Errorf("body = %q, want 'Check logs for auth in prod'", stored.Body)
	}
}

func TestProposalGetsAutoGrantedGrainCIWhenTargetHasWorkflows(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "workflows-app"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "director", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "ci proposal", ProposedBy: parent.ID, Repo: repo.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(proposal.Capabilities, "grain-ci") {
		t.Errorf("proposal capabilities = %v, want grain-ci auto-granted", proposal.Capabilities)
	}
}

func TestProposalRejectsUnknownRequestedCapability(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "parent", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "bad cap proposal", ProposedBy: parent.ID,
		Capabilities: &[]string{"non-existent-capability-1234"},
	})
	if err == nil {
		t.Fatal("expected error requesting non-existent capability on proposal, got nil")
	}
}

// A proposal filed live is held for a person when it reads or lands
// somewhere its proposer could not, exactly as the end-of-run relay holds
// it (orchestrator.relayProposedTasks, #1437, #2378): neither an
// auto-merge parent's permission nor a session's may queue it, and its
// body says which repositories are the reason. Reads gate only the
// queueing, so the reads proposal keeps the auto-merge it would inherit.
func TestLiveProposalWideningReadsOrWritesWaitsForAPerson(t *testing.T) {
	t.Parallel()
	for _, session := range []bool{false, true} {
		client, store, ctx := testClient(t)
		var parent ui.Task
		var err error
		if session {
			cfg := model.DefaultConfig()
			cfg.ConsoleSessions = []model.ConsoleSession{
				{ID: model.DirectorSessionID, Title: "Director", TasksApproved: true},
			}
			if err := store.PutConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			parent, err = client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Director", Interactive: true})
			if err != nil {
				t.Fatal(err)
			}
			stored := mustGetTask(t, ctx, store, parent.ID)
			stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
			if err := store.PutTask(ctx, stored); err != nil {
				t.Fatal(err)
			}
		} else {
			parent, err = client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "the whole job", Approved: true, AutoMerge: true,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			req           ui.CreateTaskRequest
			note          string
			wantAutoMerge bool
		}{
			{ui.CreateTaskRequest{Title: "reads", Reads: []string{"acme/secret-sauce"}},
				"asks to read `acme/secret-sauce`", !session},
			{ui.CreateTaskRequest{Title: "writes", Writes: []ui.WriteTarget{{Repo: "acme/gadgets", Base: "main"}}},
				"declares `main` in `acme/gadgets`", false},
		} {
			tc.req.Description, tc.req.ProposedBy, tc.req.AutoMerge = "d", parent.ID, true
			filed, err := client.CreateTask(ctx, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != model.StateProposed {
				t.Errorf("session=%v %s: state = %s, want proposed", session, tc.req.Title, filed.State)
			}
			if proposal := mustGetTask(t, ctx, store, filed.ID); proposal.Approval != nil {
				t.Errorf("session=%v %s: approved by %+v, want nobody", session, tc.req.Title, proposal.Approval)
			}
			if !strings.Contains(filed.Description, tc.note) {
				t.Errorf("session=%v %s: body = %q, want a note containing %q", session, tc.req.Title, filed.Description, tc.note)
			}
			if filed.AutoMerge != tc.wantAutoMerge {
				t.Errorf("session=%v %s: autoMerge = %v, want %v", session, tc.req.Title, filed.AutoMerge, tc.wantAutoMerge)
			}
		}
	}
}

// A proposal is its proposer's root requester's work: it is filed as
// that requester -- whoever the request names -- so local policy never
// takes it for grain's own (GSA1-02).
func TestProposalIsFiledAsItsProposersRequester(t *testing.T) {
	client, store, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, parent.ID, func(t *model.Task) error {
		t.Creator = "friend@example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	piece, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a piece of it", ProposedBy: parent.ID, Creator: "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, piece.ID)
	if err != nil || got == nil {
		t.Fatalf("reading the proposal: %v", err)
	}
	if got.Creator != "friend@example.com" {
		t.Errorf("creator = %q, want its proposer's requester", got.Creator)
	}
	if a := got.Approval; a != nil && a.Actor.Kind == model.PrincipalHuman {
		t.Errorf("approved by a human (%s) nobody asked", a.Actor.ID)
	}
}
