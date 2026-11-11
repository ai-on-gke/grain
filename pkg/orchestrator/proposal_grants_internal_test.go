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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRelayProposedTasksUnifiedGrantsEquivalenceAndNormalization(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	appRepo := model.RepoRef{Owner: "acme", Name: "app"}
	siteRepo := model.RepoRef{Owner: "acme", Name: "site"}
	for _, r := range []model.RepoRef{widgets, appRepo, siteRepo} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: r}); err != nil {
			t.Fatal(err)
		}
	}

	parent := model.Task{
		ID:        "10",
		Title:     "Legacy parent",
		Body:      "Parent holding legacy api scope + write target",
		Target:    &widgets,
		Base:      "main",
		Writes:    []model.WriteTarget{{Repo: appRepo, Base: "main"}},
		Grants:    []model.Grant{{Capability: "api:tasks:read@acme/app", Via: model.GrantByLabel}},
		AutoMerge: true,
		Approval:  &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}},
	}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}

	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "equiv-grants",
				"title":      "Equivalent via grants",
				"body":       "Covered by parent",
				"auto_merge": true,
				"grants": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read", "with": map[string]any{"repo": "acme/app"}},
					map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "main"}},
				},
			},
		},
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "widen-scope",
				"title":      "Widened scope via grants",
				"body":       "Not covered by parent",
				"auto_merge": true,
				"grants": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:write", "with": map[string]any{"repo": "acme/app"}},
					map[string]any{"capability": "https://grain.dev/capabilities/repo#target", "with": map[string]any{"repo": "acme/app", "base": "main"}},
				},
			},
		},
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":         "caps-api-ref",
				"title":      "Canonical api ref in capabilities",
				"body":       "Must store in api:<scope>@<repo> form",
				"repo":       "acme/app",
				"branch":     "main",
				"auto_merge": true,
				"capabilities": []any{
					map[string]any{"capability": "https://grain.dev/capabilities/api#tasks:read", "with": map[string]any{"repo": "acme/app"}},
				},
			},
		},
		{
			Name: "propose_task",
			Arguments: map[string]any{
				"id":           "bare-legacy-cap",
				"title":        "Bare legacy capability name",
				"body":         "Expands one-release retired aliases like ui.Client.CreateTask",
				"repo":         "acme/app",
				"branch":       "main",
				"capabilities": []any{"self-repair", "self-debug"},
			},
		},
	}}

	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}
	// Replay a second time to verify ProposalKeyArgs deduplicates rawP cleanly.
	if err := relayProposedTasks(ctx, store, nil, nil, parent, result, now); err != nil {
		t.Fatalf("second relayProposedTasks: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, tk := range tasks {
		if tk.ID != parent.ID {
			byTitle[tk.Title] = tk
		}
	}
	if len(byTitle) != 4 {
		t.Fatalf("filed %d child tasks (%v), want 4", len(byTitle), byTitle)
	}

	equiv := byTitle["Equivalent via grants"]
	if equiv.Approval == nil || !equiv.AutoMerge {
		t.Errorf("equiv proposal held unexpectedly: approval=%+v autoMerge=%v", equiv.Approval, equiv.AutoMerge)
	}
	if equiv.Target == nil || equiv.Target.String() != "acme/app" || equiv.Base != "main" {
		t.Errorf("equiv target = %v base=%q, want acme/app@main", equiv.Target, equiv.Base)
	}

	widened := byTitle["Widened scope via grants"]
	if widened.Approval != nil || widened.AutoMerge {
		t.Errorf("widened proposal auto-approved unexpectedly: approval=%+v autoMerge=%v", widened.Approval, widened.AutoMerge)
	}

	capsRef := byTitle["Canonical api ref in capabilities"]
	if capsRef.Approval == nil || !capsRef.AutoMerge {
		t.Errorf("caps-api-ref proposal held unexpectedly: approval=%+v autoMerge=%v", capsRef.Approval, capsRef.AutoMerge)
	}
	if want, got := model.APIGrantString("tasks:read", "acme/app"), model.GrantsBeyondCIReadDefault(capsRef); len(got) != 1 || got[0].Capability != want {
		t.Errorf("caps-api-ref Grants = %+v, want [%s]", capsRef.Grants, want)
	}

	bareLegacy := byTitle["Bare legacy capability name"]
	if bareLegacy.Approval != nil {
		t.Errorf("bare-legacy-cap proposal approved unexpectedly: %+v", bareLegacy.Approval)
	}
	wantExpanded := []model.Grant{
		{Capability: model.APIRef("self-debug"), Via: model.GrantByLabel},
		{Capability: model.GrainHostExecGrant, Via: model.GrantByLabel},
		{Capability: model.GrainSourceGrant, Via: model.GrantByLabel},
	}
	bareLegacy.Grants = model.GrantsBeyondCIReadDefault(bareLegacy)
	if len(bareLegacy.Grants) != len(wantExpanded) {
		t.Fatalf("bare-legacy-cap Grants = %+v, want %+v", bareLegacy.Grants, wantExpanded)
	}
	for i, want := range wantExpanded {
		if !bareLegacy.Grants[i].Equal(want) {
			t.Errorf("bare-legacy-cap Grants[%d] = %+v, want %+v", i, bareLegacy.Grants[i], want)
		}
	}
}

func TestRelayProposedTasksAgreesWithCallTimeRefusalsAndFieldNames(t *testing.T) {
	captured := logToTest(t)
	t.Cleanup(model.RegisterChangeTarget(model.ChangeTargetRegistration{Kind: "monorepo"}))

	store, ctx := openTreeStore(t)
	now := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets}); err != nil {
		t.Fatal(err)
	}
	parent := model.Task{
		ID:     "20",
		Title:  "Walkthrough parent",
		Body:   "Parent task",
		Target: &widgets,
		Base:   "main",
	}
	if err := store.PutTask(ctx, parent); err != nil {
		t.Fatal(err)
	}

	var proposeTool mcp.Tool
	for _, tool := range mcp.NewMockTools(&mcp.MockSink{}, nil) {
		if tool.Name == "propose_task" {
			proposeTool = tool
			break
		}
	}
	if proposeTool.Handler == nil {
		t.Fatal("missing propose_task in NewMockTools")
	}

	target := map[string]any{
		"capability": model.RepoTargetRef,
		"with":       map[string]any{"repo": "acme/widgets", "base": "main"},
	}
	read := map[string]any{
		"capability": model.RepoReadRef,
		"with":       map[string]any{"repo": "acme/widgets"},
	}
	write := map[string]any{
		"capability": model.RepoWriteRef,
		"with":       map[string]any{"repo": "acme/widgets", "base": "main"},
	}

	refusedShapes := []struct {
		name    string
		extra   map[string]any
		wantErr string
		wantLog string
	}{
		{
			name:    "base + repo#target (undeclared argument)",
			extra:   map[string]any{"base": "main", "grants": []any{target}},
			wantErr: `unknown argument "base" (did you mean "branch"?)`,
			wantLog: `skipping proposed task with invalid arguments: propose_task: unknown argument "base" (did you mean "branch"?)`,
		},
		{
			name:    "base without grants (undeclared argument)",
			extra:   map[string]any{"base": "release/v3.2.0"},
			wantErr: `unknown argument "base" (did you mean "branch"?)`,
			wantLog: `skipping proposed task with invalid arguments: propose_task: unknown argument "base" (did you mean "branch"?)`,
		},
		{
			name:    "writes entry with branch instead of base",
			extra:   map[string]any{"writes": []any{map[string]any{"repo": "acme/widgets", "branch": "main"}}},
			wantErr: `writes[0]: unknown field "branch" (did you mean "base"?)`,
			wantLog: `skipping proposed task with invalid arguments: propose_task: writes[0]: unknown field "branch" (did you mean "base"?)`,
		},
		{
			name:    "capabilities + empty grants",
			extra:   map[string]any{"capabilities": []any{"api:ci-read"}, "grants": []any{}},
			wantErr: "grants: grants and capabilities both name capability grants; send one",
			wantLog: "skipping proposed task with invalid grants: grants: grants and capabilities both name capability grants; send one",
		},
		{
			name:    "reads: [] + repo#read",
			extra:   map[string]any{"reads": []any{}, "grants": []any{read}},
			wantErr: "grants: https://grain.dev/capabilities/repo#read and reads both name read access; send one",
			wantLog: "skipping proposed task with invalid grants: grants: https://grain.dev/capabilities/repo#read and reads both name read access; send one",
		},
		{
			name:    "writes: [] + repo#write",
			extra:   map[string]any{"writes": []any{}, "grants": []any{write}},
			wantErr: "grants: https://grain.dev/capabilities/repo#write and writes both name write targets; send one",
			wantLog: "skipping proposed task with invalid grants: grants: https://grain.dev/capabilities/repo#write and writes both name write targets; send one",
		},
		{
			name:    "repo + repo#target",
			extra:   map[string]any{"repo": "acme/widgets", "grants": []any{target}},
			wantErr: "grants: https://grain.dev/capabilities/repo#target and the request's repo field both name the task's repository; send one",
			wantLog: "skipping proposed task with invalid grants: grants: https://grain.dev/capabilities/repo#target and the request's repo field both name the task's repository; send one",
		},
		{
			name:    "branch + repo#target",
			extra:   map[string]any{"branch": "main", "grants": []any{target}},
			wantErr: "grants: https://grain.dev/capabilities/repo#target and the request's branch field both name the task's repository; send one",
			wantLog: "skipping proposed task with invalid grants: grants: https://grain.dev/capabilities/repo#target and the request's branch field both name the task's repository; send one",
		},
		{
			name:    "no_repo + repo#target",
			extra:   map[string]any{"no_repo": true, "grants": []any{target}},
			wantErr: "grants: https://grain.dev/capabilities/repo#target and the request's no_repo field both name the task's repository; send one",
			wantLog: "skipping proposed task with invalid grants: grants: https://grain.dev/capabilities/repo#target and the request's no_repo field both name the task's repository; send one",
		},
		{
			name:    "change target + repo#target",
			extra:   map[string]any{"monorepo": map[string]any{}, "grants": []any{target}},
			wantErr: "grants: https://grain.dev/capabilities/repo#target and the request's monorepo field both name the task's repository; send one",
			wantLog: "skipping proposed task with invalid grants: grants: https://grain.dev/capabilities/repo#target and the request's monorepo field both name the task's repository; send one",
		},
		{
			name:    "change target + repo#write in grants",
			extra:   map[string]any{"monorepo": map[string]any{}, "grants": []any{write}},
			wantErr: "monorepo cannot be combined with writes",
			wantLog: "skipping proposed task with invalid grants: monorepo cannot be combined with writes",
		},
		{
			name:    "non-list grants",
			extra:   map[string]any{"grants": "api:ci-read"},
			wantErr: "grants must be a list",
			wantLog: "skipping proposed task with invalid grants: grants must be a list",
		},
		{
			name:    "duplicate repo#target",
			extra:   map[string]any{"grants": []any{target, target}},
			wantErr: "grants: more than one https://grain.dev/capabilities/repo#target",
			wantLog: "skipping proposed task with invalid grants: grants: more than one https://grain.dev/capabilities/repo#target",
		},
	}

	// Pass IsError: false on every ToolCall to exercise relayProposedTasks' own
	// guards (matching harnesses/streams that omit isError on tool steps).
	var calls []agent.ToolCall
	for _, tc := range refusedShapes {
		args := map[string]any{
			"title": "refused: " + tc.name,
			"body":  "should not be filed",
		}
		for k, v := range tc.extra {
			args[k] = v
		}
		res := proposeTool.Handler(context.Background(), args)
		if !res.IsError || !strings.Contains(res.Text, tc.wantErr) {
			t.Errorf("%s (call-time): got isError=%v %q, want substring %q", tc.name, res.IsError, res.Text, tc.wantErr)
		}
		calls = append(calls, agent.ToolCall{Name: "propose_task", Arguments: args})
	}

	controlArgs := map[string]any{
		"title":  "control: grants only",
		"body":   "should be filed",
		"grants": []any{"api:ci-read"},
	}
	if res := proposeTool.Handler(context.Background(), controlArgs); res.IsError {
		t.Fatalf("control call-time refused unexpectedly: %s", res.Text)
	}
	calls = append(calls, agent.ToolCall{Name: "propose_task", Arguments: controlArgs})

	if err := relayProposedTasks(ctx, store, nil, nil, parent, &agent.Result{ToolCalls: calls}, now); err != nil {
		t.Fatalf("relayProposedTasks: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var childTitles []string
	for _, tk := range tasks {
		if tk.ID != parent.ID {
			childTitles = append(childTitles, tk.Title)
		}
	}
	if len(childTitles) != 1 || childTitles[0] != "control: grants only" {
		t.Fatalf("filed children = %v, want only [\"control: grants only\"]", childTitles)
	}

	logged := captured.String()
	for _, tc := range refusedShapes {
		if !strings.Contains(logged, tc.wantLog) {
			t.Errorf("%s (relay log): missing %q in log:\n%s", tc.name, tc.wantLog, logged)
		}
	}
}
