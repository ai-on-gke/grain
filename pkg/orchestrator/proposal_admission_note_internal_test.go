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

// A run's proposals that admission changed at run end -- a CI read filed
// as reads, a CI push left off, a sandbox lowered to the deployment's
// maximum -- are named on the proposer's own conversation, once, where
// the run (on its next turn) and the person watching its task read it.
// Its propose_task calls only answered "Recorded", and before this only
// each proposal's own body said what happened
// (docs/test-runs/2026-11-13-proposal-admission.md, F1). A proposal filed
// as asked is not named, and a batch with nothing changed posts nothing.
func TestRelayedProposalsAdmissionChangedAreNamedOnTheProposer(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)

	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	for _, r := range []model.RepoRef{widgets, gadgets} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: r}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutConfig(ctx, model.Config{SandboxShapeLimits: model.SandboxShapeLimits{
		Max: model.SandboxShape{CPUs: 2, MemoryMB: 4096, DiskGB: 100},
	}}); err != nil {
		t.Fatal(err)
	}
	parent := model.Task{ID: "10", Title: "Parent", Body: "b", Target: &widgets, Base: "main"}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Read gadgets", "body": "b", "capabilities": []any{"repo-read:acme/gadgets"},
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Push widgets", "body": "b", "capabilities": []any{"repo-write:acme/widgets"},
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Big build", "body": "b", "sandbox_cpus": 16, "sandbox_disk_gb": 500,
		}},
		{Name: "propose_task", Arguments: map[string]any{
			"title": "Small fix", "body": "b", "sandbox_disk_gb": 50,
		}},
	}}
	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, tk := range tasks {
		ids[tk.Title] = tk.ID
	}
	comments, err := store.Comments(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, c := range comments {
		if strings.Contains(c.Body, "without something") {
			notes = append(notes, c.Body)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("parent has %d admission notes, want 1: %q", len(notes), notes)
	}
	note := notes[0]
	for _, want := range []string{
		"grain filed 3 proposals from this run",
		"task " + ids["Read gadgets"] + ` "Read gadgets"`, "`repo-read:acme/gadgets`", `reads: ["acme/gadgets"]`,
		"task " + ids["Push widgets"] + ` "Push widgets"`, "`repo-write:acme/widgets`",
		"task " + ids["Big build"] + ` "Big build"`, "2 vCPU, 100 GiB disk", "sandboxShapeLimits",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("parent note does not say %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "Small fix") {
		t.Errorf("parent note names a proposal filed as asked:\n%s", note)
	}

	// A batch admission changes nothing about posts nothing more.
	quiet := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{"title": "Another fix", "body": "b"}},
	}}
	if err := relayProposedTasks(ctx, store, nil, nil, parent, quiet, now.Add(time.Minute)); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	after, err := store.Comments(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(comments) {
		t.Errorf("a batch filed as asked added %d comment(s) to the parent", len(after)-len(comments))
	}
}

// A proposal naming a template is held to the maximum after the
// template's `sandbox:` fills it (§8's template size, §5 item 3): above
// it, it is lowered and the note puts the size down to the template as
// well as the run, rather than to the run alone; under it, it is filed
// at the template's shape with no note.
func TestRelayedProposalTemplateSandboxIsHeldToTheMaximum(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, model.Config{SandboxShapeLimits: model.SandboxShapeLimits{
		Max: model.SandboxShape{DiskGB: 100},
	}}); err != nil {
		t.Fatal(err)
	}
	for id, disk := range map[string]int{"big-disk": 200, "small-disk": 60} {
		if err := store.PutTemplate(ctx, model.Template{
			ID: id, Name: id, Title: id, Body: "b", Sandbox: model.SandboxShape{DiskGB: disk},
		}); err != nil {
			t.Fatal(err)
		}
	}
	parent := model.Task{ID: "20", Title: "Parent", Body: "b", Target: &widgets, Base: "main"}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{"title": "Big", "body": "b", "template_id": "big-disk"}},
		{Name: "propose_task", Arguments: map[string]any{"title": "Small", "body": "b", "template_id": "small-disk"}},
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
	big, small := byTitle["Big"], byTitle["Small"]
	if big.SandboxDiskGB != 100 {
		t.Errorf("Big disk = %d, want 100 (the maximum, below its template's 200)", big.SandboxDiskGB)
	}
	for _, want := range []string{"with its template's `sandbox:` filled in", "sandbox_disk_gb 200 (lowered to 100)"} {
		if !strings.Contains(big.Body, want) {
			t.Errorf("Big body does not say %q:\n%s", want, big.Body)
		}
	}
	if small.SandboxDiskGB != 60 || strings.Contains(small.Body, "lowered") {
		t.Errorf("Small = disk %d, body %q; want its template's 60 and no lowering note", small.SandboxDiskGB, small.Body)
	}
}
