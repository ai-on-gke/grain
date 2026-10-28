package sqlite_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A row written before RepoConfig.Work had columns of its own carried
// this decision in one negative bool, and that bool meant exactly one
// thing: this deployment does not publish from this repository. It has
// to arrive as the release item's own override, because the reason this
// design exists at all is that the old field was lost in a sync and
// publishing came back on with nobody having asked for it.
func TestALegacyReleaseSwitchBecomesTheReleaseItemsOverride(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, SetupCommand: "make deps"}); err != nil {
		t.Fatal(err)
	}
	// The shape the upgrade finds: the old column set, and neither of
	// the new ones ever written.
	if _, err := db.ExecContext(ctx,
		"UPDATE repo_config SET release_workflows_disabled = 1, work_ownership = '', work_overrides = ''"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if model.RunsWorkItem(got, model.ReleaseItem) {
		t.Errorf("a repository whose releases were switched off before the upgrade publishes after it")
	}
	if state, set := got.Work.Item(model.ReleaseItem); !set || state != model.OwnershipOff {
		t.Errorf("the old switch arrived as (%q, %v), want an explicit off on the release item", state, set)
	}
	if !model.RunsWorkItem(got, model.ScheduleItem("sched-7")) {
		t.Errorf("the old release switch stopped a schedule too; it was only ever about publishing")
	}

	// And the mirror in the other direction: whatever the new columns
	// say, the old one is left holding what a binary that reads only it
	// would need, so a rollback does not restart a publish somebody
	// stopped.
	off := model.RepoConfig{Repo: repo, Work: model.WorkOwnership{Default: model.OwnershipOff}}
	if err := store.PutRepoConfig(ctx, off); err != nil {
		t.Fatal(err)
	}
	var legacy bool
	if err := db.QueryRowContext(ctx, "SELECT release_workflows_disabled FROM repo_config").Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy {
		t.Errorf("a repository this deployment no longer runs would publish again on a rollback")
	}
	// A row that has been written since the upgrade is read from the new
	// columns alone: the mirror is never an input.
	if _, err := db.ExecContext(ctx, "UPDATE repo_config SET work_ownership = 'on'"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetRepoConfig(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !model.RunsWorkItem(got, model.ReleaseItem) {
		t.Errorf("the legacy mirror overruled the columns that replaced it")
	}
}
