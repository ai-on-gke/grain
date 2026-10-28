package orchestrator

// A repair or triage task is filed once per commit and then never again
// (its tag guards it), so the template it is filed from is decided once.
// A store read that fails while deciding it has to stop the filing, not
// fall back to the system template: that one carries its own AutoMerge,
// grants and network access, and a deployment that configured its repair
// template to be stricter would get the looser task for good because of
// one transient error (grain/task-1456). Nothing is written before the
// failure, so the next cycle files the task from the right template.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// brokenRepairTemplateStore is each case's store, set up with a repair
// template configured for repo and then broken by dropping table.
func brokenRepairTemplateStore(t *testing.T, repo model.RepoRef, table string) *model.Store {
	t.Helper()
	ctx := context.Background()
	store := openInternalStore(t)
	if err := store.PutCustomField(ctx, model.CustomField{Name: "customer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID:            "strict-repair",
		Name:          "Strict repair",
		Title:         "Repair {{repo}} {{branch}}",
		Body:          "Fix it.",
		AutoMerge:     false,
		NetworkAccess: model.NetworkNone,
		CustomFields:  map[string][]string{"customer": {"acme"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain, RepairTemplateID: "strict-repair",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "DROP TABLE `"+table+"`"); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestBaseBranchRepairAndTriageFileNothingWhenTheTemplateCannotBeRead(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name, table, want string
	}{
		{"template lookup fails", "template", "resolving the repair template for"},
		{"custom field lookup fails", "custom_field", "repair template's custom fields for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			t.Run("repair", func(t *testing.T) {
				store := brokenRepairTemplateStore(t, repo, tc.table)
				row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "abc1234def", Detail: "go failed"}
				err := fileBaseBranchRepair(ctx, Deps{Store: store}, row, now)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("fileBaseBranchRepair = %v, want an error containing %q", err, tc.want)
				}
				if filed, err := store.HasTaskWithTag(ctx, baseCIRepairTag(repo, "main", row.Commit)); err != nil || filed {
					t.Fatalf("a repair was filed (%v, %v) from a template that could not be read", filed, err)
				}
			})

			t.Run("release triage", func(t *testing.T) {
				store := brokenRepairTemplateStore(t, repo, tc.table)
				row := model.BaseBranchRelease{Repo: repo, Branch: "main", Commit: "abc1234def", Detail: "docker failed"}
				err := fileBaseBranchReleaseTriage(ctx, Deps{Store: store}, row, now)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("fileBaseBranchReleaseTriage = %v, want an error containing %q", err, tc.want)
				}
				if filed, err := store.HasTaskWithTag(ctx, BaseReleaseTriageTag(repo, "main", row.Commit)); err != nil || filed {
					t.Fatalf("a triage was filed (%v, %v) from a template that could not be read", filed, err)
				}
			})
		})
	}
}
