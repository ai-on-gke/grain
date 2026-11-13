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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A proposal naming a CI capability is refused it at filing, not at every
// dispatch after (task 4532, filed holding repo-read:<repo> and refused a
// sandbox each time it was dispatched): a CI read of another repository
// is filed as the reads entry a task reads it through, and any other CI
// capability is left off with grain's reason in the body.
func TestRelayedProposalIsNotFiledHoldingACIOnlyCapability(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	lib := model.RepoRef{Owner: "acme", Name: "lib"}
	for _, r := range []model.RepoRef{widgets, lib} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: r}); err != nil {
			t.Fatal(err)
		}
	}
	parent := model.Task{ID: "10", Title: "Release sync", Body: "b", Target: &widgets, Base: "main"}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Pin core", "body": "b", "capabilities": []any{"repo-read:acme/lib"},
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Push core", "body": "b", "capabilities": []any{"repo-write:acme/lib"},
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Read ref", "body": "b",
			"capabilities": []any{map[string]any{"capability": model.RepoReadRef, "with": map[string]any{"repo": "acme/lib"}}},
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Read own target", "body": "b", "capabilities": []any{"repo-read:acme/widgets"},
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Release gate", "body": "b", "capabilities": []any{model.ReleaseGateReadCapability},
		}},
	}}
	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, tk := range tasks {
		byTitle[tk.Title] = tk
	}
	for _, title := range []string{"Pin core", "Push core", "Read ref", "Read own target", "Release gate"} {
		tk, ok := byTitle[title]
		if !ok {
			t.Fatalf("%q was not filed; tasks = %v", title, byTitle)
		}
		// Dispatch's own check finds nothing to refuse on what was filed.
		for _, g := range tk.Grants {
			if why := model.TaskGrantCIOnlyRefusal(g.Capability); why != "" {
				t.Errorf("%q filed holding CI capability %s: %s", title, g.Capability, why)
			}
		}
	}
	for _, title := range []string{"Pin core", "Read ref"} {
		tk := byTitle[title]
		if !slices.ContainsFunc(tk.Reads, lib.Same) {
			t.Errorf("%q Reads = %v, want acme/lib (the task equivalent of its CI read)", title, tk.Reads)
		}
		if !strings.Contains(tk.Body, `reads: ["acme/lib"]`) {
			t.Errorf("%q body does not say its CI read became a reads entry:\n%s", title, tk.Body)
		}
		// Reading a repository the proposer itself cannot read is a
		// person's to approve, as an explicit reads entry would be.
		if tk.Approval != nil {
			t.Errorf("%q approved without a person though its proposer cannot read acme/lib", title)
		}
	}
	own := byTitle["Read own target"]
	if len(own.Reads) != 0 {
		t.Errorf("Read own target Reads = %v, want none for its own target repository", own.Reads)
	}
	if strings.Contains(own.Body, `reads: ["acme/widgets"]`) || !strings.Contains(own.Body, "already targets acme/widgets") {
		t.Errorf("Read own target body misstates how its own-repo CI read was handled:\n%s", own.Body)
	}
	gate := byTitle["Release gate"]
	if slices.ContainsFunc(gate.Grants, func(g model.Grant) bool { return g.Capability == model.ReleaseGateReadCapability }) {
		t.Errorf("Release gate Grants = %v, want %s dropped as CI-only", gate.Grants, model.ReleaseGateReadCapability)
	}
	if !strings.Contains(gate.Body, "is a CI capability, not a task grant") {
		t.Errorf("Release gate body does not explain the dropped CI grant:\n%s", gate.Body)
	}
	push := byTitle["Push core"]
	if len(push.Writes) != 0 {
		t.Errorf("Push core Writes = %v, want none: a CI push grant is not turned into somewhere the task lands", push.Writes)
	}
	if !strings.Contains(push.Body, "is a CI capability, not a task grant") || !strings.Contains(push.Body, `writes: [{repo: "acme/lib"}]`) {
		t.Errorf("Push core body does not explain the dropped grant and name its equivalent:\n%s", push.Body)
	}
}
