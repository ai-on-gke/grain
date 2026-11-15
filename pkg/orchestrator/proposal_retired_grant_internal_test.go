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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A proposal a run recorded naming retired bare names (self-debug,
// grain-ci) is filed without them, its body and its proposer's
// conversation naming what each became. It used to be filed holding the
// bare rows, held for a person whose approval "granted" them, and every
// dispatch after refused it (2026-11-14 retired-aliases walkthrough, F5).
func TestRelayedProposalLeavesOffARetiredBareNameNamingWhatItBecame(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets}); err != nil {
		t.Fatal(err)
	}
	parent := model.Task{ID: "10", Title: "Walk", Body: "b", Target: &widgets, Base: "main"}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{
			"title": "walk: bare names", "body": "b", "capabilities": []any{"self-debug", "grain-ci"},
		}},
	}}
	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var filed *model.Task
	for i := range tasks {
		if tasks[i].Title == "walk: bare names" {
			filed = &tasks[i]
		}
	}
	if filed == nil {
		t.Fatalf("the proposal was not filed; tasks = %+v", tasks)
	}
	for _, g := range filed.Grants {
		if g.Capability == "self-debug" || g.Capability == "grain-ci" {
			t.Errorf("filed holding retired bare grant %q: %+v", g.Capability, filed.Grants)
		}
	}
	for _, want := range []string{"without `self-debug`", model.APIRef("self-debug"), "without `grain-ci`", "CI Mode"} {
		if !strings.Contains(filed.Body, want) {
			t.Errorf("filed body does not say %q:\n%s", want, filed.Body)
		}
	}
	comments, err := store.Comments(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, c := range comments {
		said = said || (strings.Contains(c.Body, "`self-debug` was left off") && strings.Contains(c.Body, model.APIRef("self-debug")))
	}
	if !said {
		t.Errorf("the proposer's conversation does not name the left-off retired grant: %+v", comments)
	}
}
