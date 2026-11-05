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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The live propose_task call follows the end-of-run relay's rule for
// a qualification task's fixtures (model.ScratchFixture, task 2839): one
// on the proposer's own qualification/<id> branch, which its template may
// push, is queued and merges there even though the proposer is no
// auto-merge job; one on the default branch waits for a person.
func TestALiveScratchFixtureIsQueuedAndMergesOnItsScratchBranch(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	repo := declared
	templateID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-mergequeue")
	if _, err := store.SyncRepoDeclarations(ctx, repo, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/qualification-mergequeue.yml",
			Template: model.Template{ID: templateID, Name: "qualification-mergequeue", Title: "Qualify", Target: &repo},
			Push:     model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: templateID, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := storedTask(t, store, ctx, parent.ID); got.AutoMerge || got.AutoApproveProposals || got.TemplateID != templateID {
		t.Fatalf("parent: autoMerge=%v autoApproveProposals=%v template=%q, want neither permission, filed from %s",
			got.AutoMerge, got.AutoApproveProposals, got.TemplateID, templateID)
	}

	for _, tc := range []struct {
		name, base             string
		decline                bool
		wantQueued, wantMerges bool
	}{
		{name: "fixture", base: model.QualificationScratchBranch(parent.ID), wantQueued: true, wantMerges: true},
		{name: "held fixture", base: model.QualificationScratchBranch(parent.ID), decline: true, wantQueued: true},
		{name: "defect", base: ""},
		{name: "someone else's fixture", base: model.QualificationScratchBranch("9999")},
	} {
		child, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: tc.name, Description: "d", Repo: repo.String(), Base: tc.base,
			ProposedBy: parent.ID, DeclineAutoMerge: tc.decline,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		state, err := store.State(ctx, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		if queued := state == model.StateQueued; queued != tc.wantQueued {
			t.Errorf("%s: state = %q, want queued %v", tc.name, state, tc.wantQueued)
		}
		if got := storedTask(t, store, ctx, child.ID); got.AutoMerge != tc.wantMerges {
			t.Errorf("%s: AutoMerge = %v, want %v", tc.name, got.AutoMerge, tc.wantMerges)
		}
	}
}
