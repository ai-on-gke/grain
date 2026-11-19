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

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestUsesGitHubChecks(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	actions := ui.SandboxCLIActions{Client: c}

	seedTask := func(id string, repo model.RepoRef) {
		t.Helper()
		if err := store.PutTask(ctx, model.Task{
			ID:     id,
			Intent: model.IntentImplement,
			Title:  "task " + id,
			Target: &repo,
			Branch: "grain/task-" + id,
			Base:   "main",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// No RepoConfig row at all -> EffectiveRepoConfig.CIMode == "" -> GitHub checks.
	unconfigured := model.RepoRef{Owner: "acme", Name: "unconfigured"}
	seedTask("1", unconfigured)
	if got, err := actions.UsesGitHubChecks(ctx, "1"); err != nil || !got {
		t.Fatalf("unconfigured repo: UsesGitHubChecks = (%v, %v), want (true, nil)", got, err)
	}

	// RepoConfig row with ci_mode: "" -> GitHub checks.
	emptyMode := model.RepoRef{Owner: "acme", Name: "empty-mode"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: emptyMode, CIMode: ""}); err != nil {
		t.Fatal(err)
	}
	seedTask("2", emptyMode)
	if got, err := actions.UsesGitHubChecks(ctx, "2"); err != nil || !got {
		t.Fatalf("ci_mode=\"\": UsesGitHubChecks = (%v, %v), want (true, nil)", got, err)
	}

	// Explicit ci_mode: github -> GitHub checks.
	ghMode := model.RepoRef{Owner: "acme", Name: "gh-mode"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: ghMode, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	seedTask("3", ghMode)
	if got, err := actions.UsesGitHubChecks(ctx, "3"); err != nil || !got {
		t.Fatalf("ci_mode=github: UsesGitHubChecks = (%v, %v), want (true, nil)", got, err)
	}

	// ci_mode: grain -> false.
	grainMode := model.RepoRef{Owner: "acme", Name: "grain-mode"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: grainMode, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	seedTask("4", grainMode)
	if got, err := actions.UsesGitHubChecks(ctx, "4"); err != nil || got {
		t.Fatalf("ci_mode=grain: UsesGitHubChecks = (%v, %v), want (false, nil)", got, err)
	}

	// ci_mode: none -> false.
	noneMode := model.RepoRef{Owner: "acme", Name: "none-mode"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: noneMode, CIMode: model.CIModeNone}); err != nil {
		t.Fatal(err)
	}
	seedTask("5", noneMode)
	if got, err := actions.UsesGitHubChecks(ctx, "5"); err != nil || got {
		t.Fatalf("ci_mode=none: UsesGitHubChecks = (%v, %v), want (false, nil)", got, err)
	}

	// Config repository always uses Grain CI -> false.
	seedTask("6", model.ConfigRepo())
	if got, err := actions.UsesGitHubChecks(ctx, "6"); err != nil || got {
		t.Fatalf("config repo: UsesGitHubChecks = (%v, %v), want (false, nil)", got, err)
	}
}
