package model_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// The merge rules a pull request is judged by are the base's: what the
// last good declaration sync read off the default branch, kept through a
// sync that failed, and gone with a sync that no longer declares them.
func TestMergeRulesAreTheLastGoodSyncsAndGoWithTheirRepository(t *testing.T) {
	st := openBaseBranchTestStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if got, err := st.MergeRulesOf(ctx, repo); err != nil || got != nil {
		t.Fatalf("before any sync: %+v (%v), want none", got, err)
	}
	rules := &model.MergeRules{Path: model.MergeRulesPath, Rules: []model.MergeRule{{ID: "deploy",
		Paths: []string{"deploy/**"}, MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever}}}}
	if _, err := st.SyncRepoDeclarations(ctx, repo, "abc", model.RepoDeclarations{MergeRules: rules},
		now, time.UTC); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordDeclarationFailure(ctx, repo, ".grain/merge-rules.yml: bad", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.MergeRulesOf(ctx, repo)
	want := *rules
	want.Commit = "abc"
	if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("after a failed sync: %+v (%v), want %+v", got, err, want)
	}
	if !model.TriggerDeclarationsBroken(".grain/merge-rules.yml: bad") {
		t.Fatal("a base whose merge rules do not parse must hold its changes, as broken triggers do")
	}
	if _, err := st.SyncRepoDeclarations(ctx, repo, "def", model.RepoDeclarations{}, now.Add(2*time.Minute),
		time.UTC); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.MergeRulesOf(ctx, repo); got != nil {
		t.Fatalf("withdrawn merge rules are still stored: %+v", got)
	}
}

// A sync at a new base head whose rules say what the stored ones say
// keeps the commit they were read at, so a merge does not invalidate
// every open change's classification (EffectiveMergePolicy.Rules); rules
// that change take the new commit.
func TestUnchangedMergeRulesKeepTheirCommit(t *testing.T) {
	st := openBaseBranchTestStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rules := func(id string) *model.MergeRules {
		return &model.MergeRules{Path: model.MergeRulesPath, Rules: []model.MergeRule{{ID: id,
			Paths: []string{"deploy/**"}, MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever}}}}
	}
	for i, step := range []struct{ commit, rule, want string }{
		{"c1", "deploy", "c1"},
		{"c2", "deploy", "c1"},
		{"c3", "release", "c3"},
	} {
		if _, err := st.SyncRepoDeclarations(ctx, repo, step.commit,
			model.RepoDeclarations{MergeRules: rules(step.rule)}, now.Add(time.Duration(i)*time.Minute), time.UTC); err != nil {
			t.Fatal(err)
		}
		got, err := st.MergeRulesOf(ctx, repo)
		if err != nil || got == nil || got.Commit != step.want || got.Rules[0].ID != step.rule {
			t.Fatalf("after syncing %s: %+v (%v), want rule %s at %s", step.commit, got, err, step.rule, step.want)
		}
	}
}
