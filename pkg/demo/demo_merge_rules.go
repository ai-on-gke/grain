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

package demo

// The merge-rules demo is the fixture behind
// ui/e2e/merge-rules-walkthrough.spec.js (docs/test-plans/2026-10-05.md
// §2 step 5): acme/widgets declaring the docs/declarations.md example as
// its .grain/merge-rules.yml, and two deploy/** changes the merge queue
// has classified under it -- one waiting on a required GitHub check that
// never reported, its auto-merge withdrawn for `auto_merge: never`, and
// one a person submitted that the queue then gave up on after the rule's
// 30m `stall_timeout`. Opt-in,
// so the ordinary demo's task list is unchanged.
//
// The repo also still carries a .grain/sensitive.yml, the older name for
// the same file, so the Repos page has the warning the sync keeps when
// both exist to show (docs/test-plans/2026-10-06.md §6 step 4).
//
// The rules are parsed by repodecl and synced through the store as the
// declaration sync does; each policy is model.MergeRules.Effective over
// the change's files, the comment is the queue's own
// (orchestrator.MergePolicyNote), and the rows are written as the queue
// writes them. tests/e2e/merge_rules_walkthrough_test.go reaches the
// same states through the real queue over a git server.

import (
	"context"
	"fmt"
	"slices"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const (
	demoMergeRulesWaitingTitle = "Roll the ingress controller to v2"
	demoMergeRulesStalledTitle = "Raise the API pod memory limit"
)

// demoMergeRulesFile is docs/declarations.md's example, verbatim.
const demoMergeRulesFile = `# .grain/merge-rules.yml
defaults:                      # every file no rule matches, and the PR as a whole
  stall_timeout: 2h
rules:
  - id: docs
    description: Documentation only needs lint.
    paths: ["docs/**"]
    ci:
      grain: [lint]
  - id: ui
    paths: ["ui/**"]
    ci:
      grain: ["ui e2e"]
  - id: deploy
    paths: ["deploy/**"]
    ci:
      mode: both
      github: [build, cla/google]
    auto_merge: never
    stall_timeout: 30m
`

// demoLeftoverSensitiveFile is a .grain/sensitive.yml nobody deleted when
// the rules moved to merge-rules.yml. It is ignored, and says so.
const demoLeftoverSensitiveFile = `# .grain/sensitive.yml
rules:
  - id: deploy
    paths: ["deploy/**"]
    auto_merge: never
`

func seedMergeRulesDemo(ctx context.Context, store *model.Store, cfg ui.Config, repo model.RepoRef) error {
	const commit = "5e1d2c3b4a5968778695a4b3c2d1e0f05e1d2c3b"
	parsed, err := repodecl.LoadMergeRules(fstest.MapFS{
		model.MergeRulesPath:      &fstest.MapFile{Data: []byte(demoMergeRulesFile)},
		model.MergeRulesAliasPath: &fstest.MapFile{Data: []byte(demoLeftoverSensitiveFile)},
	})
	if err != nil {
		return fmt.Errorf("parsing the example merge rules: %w", err)
	}
	now := time.Now().UTC()
	rc, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return err
	}
	if rc == nil {
		rc = &model.RepoConfig{Repo: repo}
	}
	rc.Declarations, rc.CIMode = true, model.CIModeGrain
	if err := store.PutRepoConfig(ctx, *rc); err != nil {
		return fmt.Errorf("configuring %s: %w", repo, err)
	}
	// Existing tasks on repo were seeded before CIMode became "grain", so
	// grant them the default grain-ci capability that new tasks on a
	// grain-gated repo receive at creation (Client.creationGrants).
	if existing, err := store.ListTasks(ctx); err == nil {
		hasGrainCI := func(grants []model.Grant) bool {
			return slices.ContainsFunc(grants, func(g model.Grant) bool {
				return g.Capability == grainci.CapabilityName
			})
		}
		for _, t := range existing {
			if t.Target != nil && repo.Same(*t.Target) && !hasGrainCI(t.Grants) {
				_ = store.UpdateTask(ctx, t.ID, func(task *model.Task) error {
					if !hasGrainCI(task.Grants) {
						task.Grants = append(task.Grants, model.Grant{
							Capability: grainci.CapabilityName,
							Via:        model.GrantByDefault,
						})
					}
					return nil
				})
			}
		}
	}
	if err := syncDemoDeclarations(ctx, store, repo, commit,
		model.RepoDeclarations{MergeRules: parsed}, now.Add(-2*time.Hour)); err != nil {
		return fmt.Errorf("seeding %s's merge rules: %w", repo, err)
	}
	rules, err := store.MergeRulesOf(ctx, repo)
	if err != nil || rules == nil {
		return fmt.Errorf("reading back %s's merge rules: %v", repo, err)
	}

	client := ui.NewClient(cfg, store)
	seed := func(title, file, pr, head string, submitted bool, row func(o *model.Observation, at time.Time)) error {
		created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Description: "Change " + file + ". Seeded by grain demo -merge-rules.",
			Repo: repo.String(), Approved: true, AutoMerge: true,
		})
		if err != nil {
			return fmt.Errorf("seeding %q: %w", title, err)
		}
		p := rules.Effective([]string{file}, true, model.CIModeGrain)
		p.Head = head
		classified := now.Add(-50 * time.Minute)
		if err := store.UpdateTask(ctx, created.ID, func(t *model.Task) error {
			// `auto_merge: never` withdrew it for this head; a person's
			// Submit is what turns it back on.
			t.AutoMerge = submitted
			t.Links = append(t.Links,
				model.Link{Kind: model.LinkFixes, Target: pr},
				model.MergePolicyLink(p),
				model.Link{Kind: model.LinkMergePolicyWithdrawn, Target: head})
			return nil
		}); err != nil {
			return fmt.Errorf("classifying %q: %w", title, err)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    created.ID,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}},
			Body:      orchestrator.MergePolicyNote(rules, p),
			CreatedAt: classified,
		}); err != nil {
			return fmt.Errorf("noting %q's merge policy: %w", title, err)
		}
		at := now.Add(-20 * time.Minute)
		return store.ObserveField(ctx, created.ID, at, func(o *model.Observation) {
			o.CompletedAt = &classified
			row(o, at)
		})
	}
	if err := seed(demoMergeRulesWaitingTitle, "deploy/ingress.yaml", repo.String()+"#131",
		"a1b2c3d4e5f60718293a4b5c6d7e8f9001122334", false, func(o *model.Observation, at time.Time) {
			o.MergeQueueState, o.MergeQueueStateAt = "waiting_ci", &at
			o.MergeQueueReason = "waiting_ci/github_checks_pending"
			o.MergeQueueNote = "waiting on required GitHub check(s) not reported on this commit at all: " +
				"`cla/google` (required by `deploy`)"
		}); err != nil {
		return err
	}
	return seed(demoMergeRulesStalledTitle, "deploy/api.yaml", repo.String()+"#132",
		"b2c3d4e5f60718293a4b5c6d7e8f900112233445", true, func(o *model.Observation, at time.Time) {
			o.MergeQueueState, o.MergeQueueStateAt = "merge_failed", &at
			o.MergeQueueReason = "merge_failed/given_up"
			o.MergeQueueBlockedAt = &at
			o.MergeQueueNote = "gave up waiting on CI after 30m: checks that have not finished (`cla/google`) -- " +
				"\"Retry merge\" starts the round again"
		})
}
