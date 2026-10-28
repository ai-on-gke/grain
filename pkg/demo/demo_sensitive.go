package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// seedSensitiveHoldDemo is `grain demo -sensitive-hold`: the state the
// orchestrator leaves behind when a repository's sensitive path trigger
// holds a change (orchestrator's pathtriggerhold.go), and a declaration
// sync that has since failed on a malformed trigger file -- so the task
// pane's hold, its release by an approver's Submit and the repository
// page's declarations status can be walked in a browser
// (docs/test-runs/2026-10-04-path-triggers-sensitive-paths.md,
// ui/e2e/path-triggers-sensitive-hold-walkthrough.spec.js). Opt-in,
// because it adds tasks every other demo spec would otherwise have to
// count around.
//
// The hold itself is the orchestrator's to record, and tests/e2e's
// path-trigger walkthrough records it the real way; here it is written
// as the links that walkthrough asserts on, which is all the UI and
// ui.Client.Submit read.
//
// A single-tenant demo (-access-code-file, store.SingleTenant) keeps no
// team besides default and no people besides its owner, so there the
// trigger names the owner (cfg.Actor) as its one approver, and the
// owner's Submit is the release.
//
// githubSimRepo is -github-sim's bare repo, or "" without it. The review
// is filed on the held change's branch, which a real hold has already
// pushed; with the simulated GitHub answering the base check for real,
// the branch is pushed there first. (Making the review depend on the
// held task instead would block it on the very change it gates.)
func seedSensitiveHoldDemo(ctx context.Context, store *model.Store, cfg ui.Config, repo model.RepoRef, githubSimRepo string) error {
	approvers := []string{cfg.Actor.ID}

	now := time.Now().UTC()
	trigger := model.PathTrigger{
		Name: "payments", Path: model.TriggerDeclarationDir + "/payments.yml",
		On: model.TriggerOnPullRequest, Paths: []string{"src/payments/**"},
		Gate: true, Sensitive: true, Human: model.TriggerHumanRequired,
		Approvers: approvers, Template: model.SecurityReviewTemplateID,
	}
	rc, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return err
	}
	if rc == nil {
		rc = &model.RepoConfig{Repo: repo}
	}
	rc.Declarations = true
	if err := store.PutRepoConfig(ctx, *rc); err != nil {
		return fmt.Errorf("turning declarations on for %s: %w", repo, err)
	}
	if err := syncDemoDeclarations(ctx, store, repo, "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c",
		model.RepoDeclarations{Triggers: []model.PathTrigger{trigger}}, now.Add(-time.Hour)); err != nil {
		return fmt.Errorf("seeding %s's payments trigger: %w", repo, err)
	}
	// A later commit added a trigger file that does not parse: the sync
	// records why and keeps the payments trigger above.
	if err := store.RecordDeclarationFailure(ctx, repo,
		"reading "+repo.String()+"'s declarations: .grain/triggers/broken.yml: yaml: line 2: did not find expected ',' or ']'",
		now); err != nil {
		return fmt.Errorf("seeding a failed declaration read: %w", err)
	}

	client := ui.NewClient(cfg, store)
	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Charge in cents",
		Description: "Store payment amounts as integer cents in src/payments.",
		Repo:        repo.String(),
		Approved:    true,
		AutoMerge:   true,
	})
	if err != nil {
		return fmt.Errorf("seeding the held change: %w", err)
	}
	if githubSimRepo != "" {
		if err := pushDemoGitHubSimBranch(githubSimRepo, model.BranchName(held.ID)); err != nil {
			return fmt.Errorf("pushing the held change's branch: %w", err)
		}
	}
	review, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Security review: Charge in cents",
		Description: "Review " + model.BranchName(held.ID) + " for its change to `src/payments/x.go`.",
		Repo:        repo.String(),
		Base:        model.BranchName(held.ID),
		Approved:    true,
	})
	if err != nil {
		return fmt.Errorf("seeding the held change's review: %w", err)
	}
	if err := store.UpdateTask(ctx, held.ID, func(t *model.Task) error {
		t.AutoMerge = false
		t.Links = append(t.Links,
			model.Link{Kind: model.LinkFixes, Target: repo.String() + "#105"},
			model.Link{Kind: model.LinkPathTriggerHold, Target: string(model.TriggerHumanRequired)},
			model.Link{Kind: model.LinkPathTriggerTask, Target: "payments=" + review.ID},
			model.ApprovalRequiredLink(model.ApprovalRuleTrigger+"payments", trigger.Approvers))
		return nil
	}); err != nil {
		return fmt.Errorf("holding the seeded change: %w", err)
	}
	finished := now.Add(-10 * time.Minute)
	if err := store.ObserveField(ctx, held.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		return fmt.Errorf("finishing the held change's run: %w", err)
	}
	return nil
}
