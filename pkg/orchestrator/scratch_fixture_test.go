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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A release-qualification task files its fixtures against its own
// qualification/<task-id> scratch branch, with auto-merge off for the
// default branch and no auto-approve-proposals at all
// (model.ScratchFixture, task 2839). Its template's push set covering that
// branch is the permission: those fixtures are queued, and merge there,
// with nobody clicking -- and nothing else it proposes is.
func TestProcessResultQueuesAQualificationTasksScratchFixtures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		push          model.RefPushSet
		args          map[string]any
		wantQueued    bool
		wantAutoMerge bool
	}{
		{
			name:       "a fixture on its own scratch branch",
			push:       model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
			args:       map[string]any{"branch": "qualification/t1"},
			wantQueued: true, wantAutoMerge: true,
		},
		{
			name:       "a fixture that asked not to merge",
			push:       model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
			args:       map[string]any{"branch": "qualification/t1", "auto_merge": false},
			wantQueued: true, wantAutoMerge: false,
		},
		{
			name: "a defect report on the default branch",
			push: model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
			args: map[string]any{},
		},
		{
			name: "a fixture on another task's scratch branch",
			push: model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
			args: map[string]any{"branch": "qualification/t9"},
		},
		{
			name: "a fixture on a branch below its scratch branch",
			push: model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
			args: map[string]any{"branch": "qualification/t1/extra"},
		},
		{
			name: "a fixture from a template that may not push the branch",
			push: model.RefPushSet{{Pattern: "refs/heads/candidate"}},
			args: map[string]any{"branch": "qualification/t1"},
		},
		{
			name: "a fixture asking for write targets of its own",
			push: model.RefPushSet{{Pattern: "refs/heads/qualification/*", Delete: true}},
			args: map[string]any{
				"branch": "qualification/t1",
				"writes": []any{map[string]any{"repo": "acme/widgets", "base": "main"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			templateID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-mergequeue")
			if _, err := store.SyncRepoDeclarations(ctx, repo, "abc123", model.RepoDeclarations{
				Templates: []model.DeclaredTemplateFile{{
					Path:     model.TemplateDeclarationDir + "/qualification-mergequeue.yml",
					Template: model.Template{ID: templateID, Name: "qualification-mergequeue", Title: "Qualify", Target: &repo},
					Push:     tc.push,
				}},
			}, time.Now().UTC(), time.UTC); err != nil {
				t.Fatalf("syncing declarations: %v", err)
			}
			task := filedTask(t, ctx, store, "t1", repo)
			task.TemplateID = templateID
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatalf("filing t1 from the template: %v", err)
			}

			args := map[string]any{"title": "fixture", "body": "a change for the queue"}
			for k, v := range tc.args {
				args[k] = v
			}
			result := toolResult(agent.ToolCall{Name: "propose_task", Arguments: args})
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["fixture"]
			if !ok {
				t.Fatal("the proposed task was never filed")
			}
			state, err := store.State(ctx, proposal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if queued := state == model.StateQueued; queued != tc.wantQueued {
				t.Errorf("state = %q, want queued %v", state, tc.wantQueued)
			}
			if tc.wantQueued && (proposal.Approval == nil || proposal.Approval.Actor != model.AutoSubmitPrincipal) {
				t.Errorf("approval = %+v, want %+v", proposal.Approval, model.AutoSubmitPrincipal)
			}
			if proposal.AutoMerge != tc.wantAutoMerge {
				t.Errorf("AutoMerge = %v, want %v", proposal.AutoMerge, tc.wantAutoMerge)
			}
		})
	}
}
