package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestPathTriggersSurviveAFailedSyncAndGoWithTheirRepository(t *testing.T) {
	st := openBaseBranchTestStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	trigger := model.PathTrigger{Name: "design", Path: ".grain/triggers/design.yml", On: model.TriggerOnMerge,
		Paths: []string{"docs/design/**"}, Template: "template-1", Per: model.TriggerPerFolder}
	if _, err := st.SyncRepoDeclarations(ctx, repo, "abc", model.RepoDeclarations{
		Triggers: []model.PathTrigger{trigger}}, now, time.UTC); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPathTriggerCursor(ctx, repo, "main", "def", now); err != nil {
		t.Fatal(err)
	}

	// A commit whose trigger file does not parse is a failure, and the
	// last good commit's triggers keep running.
	if err := st.RecordDeclarationFailure(ctx, repo, "bad trigger", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.PathTriggersOf(ctx, repo)
	if err != nil || len(got) != 1 || got[0].Commit != "abc" || got[0].Trigger.Name != "design" ||
		got[0].Trigger.Per != model.TriggerPerFolder {
		t.Fatalf("after a failed sync: %+v (%v)", got, err)
	}

	// A sync that no longer declares it removes it.
	if _, err := st.SyncRepoDeclarations(ctx, repo, "ghi", model.RepoDeclarations{}, now.Add(2*time.Minute), time.UTC); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PathTriggersOf(ctx, repo); len(got) != 0 {
		t.Fatalf("a withdrawn trigger is still stored: %+v", got)
	}
	// The cursor is not the declaration's to clear: a sync happens on
	// every merge, and clearing it would skip that merge.
	if c, _ := st.GetPathTriggerCursor(ctx, repo, "main"); c != "def" {
		t.Fatalf("a sync moved the cursor to %q", c)
	}

	// And turning declarations off forgets the cursor with them.
	if err := st.ForgetRepoDeclarations(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetPathTriggerCursor(ctx, repo, "main"); c != "" {
		t.Fatalf("cursor survived forgetting the repository: %q", c)
	}
}
