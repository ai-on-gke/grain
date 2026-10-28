package demo

import (
	"context"
	"fmt"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoMechanicalPortSourceTitle is the merged change the seeded port
// carries across.
const demoMechanicalPortSourceTitle = "Retry the merge queue's mergeability read once"

// seedMechanicalPortDemo is `grain demo -mechanical-port`: what the
// orchestrator leaves behind when its porter (pkg/portpick) has
// cherry-picked a change merged onto main across to candidate itself, with
// no agent run -- so the task pane, the inbox and the repository page can
// be read in a browser for a task that owns its pull request and has never
// had an attempt (docs/test-runs/2026-10-07-mechanical-porter.md). Opt-in,
// because it adds two tasks every other demo spec would otherwise have to
// count around.
//
// tests/e2e's port_mechanical_walkthrough_test.go files such a port the
// real way; here it is written as the rows that walkthrough asserts on
// (port.go's newPortTask, portMechanically and fileMechanicalPort), which
// is all the UI reads.
func seedMechanicalPortDemo(ctx context.Context, store *model.Store, cfg ui.Config, repo model.RepoRef) error {
	client := ui.NewClient(cfg, store)
	source, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       demoMechanicalPortSourceTitle,
		Description: "A transient 502 from GitHub's mergeability read parks a clean pull request for a whole cycle.",
		Repo:        repo.String(),
		Base:        "main",
		Approved:    true,
		AutoMerge:   true,
	})
	if err != nil {
		return fmt.Errorf("seeding the merged change: %w", err)
	}
	now := time.Now().UTC()
	merged := now.Add(-20 * time.Minute)
	sourcePR := model.PullRequestRef{Repo: repo, Number: 140}
	portPR := model.PullRequestRef{Repo: repo, Number: 141}
	sourceHead := "5f0c2a7d9e14b3c6a8f1d2e3b4c5a6f7e8d9c0b1"

	id, err := store.NewTaskID(ctx)
	if err != nil {
		return err
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return err
	}
	porter := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}}
	picked := fmt.Sprintf("cherry-picked 1 commit(s) onto `candidate`: %s -> 9a8b7c6d5e4f", sourceHead[:12])
	port := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  fmt.Sprintf("Port %s to candidate: %s", sourcePR, demoMechanicalPortSourceTitle),
		Body: fmt.Sprintf("Task %s's pull request %s (%q) merged onto `main`. `%s` keeps `main` and `candidate` paired, "+
			"so the same change belongs on `candidate` too. Port it there.\n\n"+
			"grain has already made this port itself, with no agent: it %s, pushed the result as `%s`, and opened "+
			"the pull request from it.", source.ID, sourcePR, demoMechanicalPortSourceTitle, repo, picked, model.BranchName(id)),
		Origin:     model.Origin{Attribution: porter, Reason: model.ReasonDirect},
		Approval:   &porter,
		ApprovedAt: &merged,
		Target:     &repo,
		Binding:    model.BindingDirective,
		Base:       "candidate",
		Branch:     model.BranchName(id),
		AutoMerge:  true,
		Tags:       []string{orchestrator.PortTag(repo, source.ID, "candidate"), model.TagMechanicalPort},
		Links: []model.Link{
			{Kind: model.LinkPortOf, Target: source.ID},
			{Kind: model.LinkFixes, Target: portPR.String()},
		},
		OrderKey:  orderKey,
		CreatedAt: &merged,
	}
	if err := store.PutTask(ctx, port); err != nil {
		return fmt.Errorf("seeding the mechanical port: %w", err)
	}
	if err := store.ObserveField(ctx, port.ID, merged, func(o *model.Observation) {
		o.CompletedAt = &merged
		o.PrOpenedAt = &merged
	}); err != nil {
		return fmt.Errorf("finishing the mechanical port: %w", err)
	}

	// The change was filed well before it merged; CreateTask stamped it
	// now.
	filed := merged.Add(-2 * time.Hour)
	if err := store.UpdateTask(ctx, source.ID, func(t *model.Task) error {
		t.CreatedAt = &filed
		t.Links = append(t.Links,
			model.Link{Kind: model.LinkFixes, Target: sourcePR.String()},
			model.Link{Kind: model.LinkPortedBy, Target: port.ID})
		return nil
	}); err != nil {
		return fmt.Errorf("linking the merged change: %w", err)
	}
	finished := merged.Add(-30 * time.Minute)
	if err := store.ObserveField(ctx, source.ID, merged, func(o *model.Observation) {
		o.CompletedAt = &finished
		o.PrMergedAt = &merged
		o.ClosedAt = &merged
	}); err != nil {
		return fmt.Errorf("merging the seeded change: %w", err)
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: source.ID,
		Author: porter,
		Body: fmt.Sprintf("Merged onto `main`, which `%s` keeps paired with `candidate`: %s ported this change there "+
			"by cherry-picking it, with no agent run, and its pull request is in the merge queue.", repo, port.ID),
		CreatedAt: merged,
	}); err != nil {
		return fmt.Errorf("noting the port: %w", err)
	}
	return seedPortSettingsDemo(ctx, store, repo, merged)
}

// demoPortSettingsFile is the `.grain/settings.yml` the seeded repository
// declares: a list widening the operator's main->candidate filter, and
// two entries that cannot take effect -- a list on candidate->main, a
// direction the operator pairs with no list of its own, and a typo.
const demoPortSettingsFile = `port-types:
  main->candidate: [fix, revert]
  candidate->main: [fix]
  main->candidtae: [fix]
`

// seedPortSettingsDemo gives repo the port configuration the porter
// above acts on, so the repository page's read-only port section and its
// port-types warnings can be read in a browser
// (docs/test-runs/2026-10-14-port-settings-revert-porting.md): the
// operator pairs main and candidate and narrows main->candidate to
// fixes, as a grain/config state repository import would write it, and
// the repository's own settings file is read the way the declaration
// sync reads it.
func seedPortSettingsDemo(ctx context.Context, store *model.Store, repo model.RepoRef, now time.Time) error {
	rc, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return err
	}
	if rc == nil {
		rc = &model.RepoConfig{Repo: repo}
	}
	rc.Declarations = true
	rc.PortBranches = []string{"main", "candidate"}
	rc.PortTypes = map[string][]string{"main->candidate": {"fix"}}
	if err := store.PutRepoConfig(ctx, *rc); err != nil {
		return fmt.Errorf("pairing %s's branches: %w", repo, err)
	}
	settings, err := repodecl.LoadSettings(fstest.MapFS{
		".grain/settings.yml": &fstest.MapFile{Data: []byte(demoPortSettingsFile)},
	}, repo)
	if err != nil {
		return fmt.Errorf("parsing the example port-types: %w", err)
	}
	if err := syncDemoDeclarations(ctx, store, repo, "7d1c0e5b9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d",
		model.RepoDeclarations{Settings: settings}, now); err != nil {
		return fmt.Errorf("seeding %s's port-types: %w", repo, err)
	}
	return nil
}
