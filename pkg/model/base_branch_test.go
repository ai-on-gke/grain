package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openBaseBranchTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := model.New(db)
	if err := st.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return st
}

func TestBaseBranchCI_BrokenAndConfirmed(t *testing.T) {
	var nilCI *model.BaseBranchCI
	if nilCI.Broken() {
		t.Error("nil BaseBranchCI should not be Broken()")
	}

	ciSuccess := &model.BaseBranchCI{Conclusion: "success"}
	if ciSuccess.Broken() {
		t.Error("success BaseBranchCI should not be Broken()")
	}
	if ciSuccess.Confirmed() {
		t.Error("success BaseBranchCI should not be Confirmed()")
	}

	ciFailure := &model.BaseBranchCI{Conclusion: "failure"}
	if !ciFailure.Broken() {
		t.Error("failure BaseBranchCI should be Broken()")
	}
	if ciFailure.Confirmed() {
		t.Error("unconfirmed failure BaseBranchCI should not be Confirmed()")
	}

	ciCleared := &model.BaseBranchCI{Conclusion: "failure", Confirmation: model.BaseCICleared}
	if ciCleared.Broken() {
		t.Error("cleared failure BaseBranchCI should not be Broken()")
	}

	ciConfirmed := &model.BaseBranchCI{Conclusion: "failure", Confirmation: model.BaseCIConfirmed}
	if !ciConfirmed.Broken() {
		t.Error("confirmed failure BaseBranchCI should be Broken()")
	}
	if !ciConfirmed.Confirmed() {
		t.Error("confirmed failure BaseBranchCI should be Confirmed()")
	}
}

func TestBaseBranchDispatch_InFlight(t *testing.T) {
	dEmpty := model.BaseBranchDispatch{}
	if dEmpty.InFlight() {
		t.Error("empty BaseBranchDispatch should not be InFlight()")
	}

	dInFlight := model.BaseBranchDispatch{RunID: "run-1"}
	if !dInFlight.InFlight() {
		t.Error("BaseBranchDispatch with RunID and empty Conclusion should be InFlight()")
	}

	dFinished := model.BaseBranchDispatch{RunID: "run-1", Conclusion: "success"}
	if dFinished.InFlight() {
		t.Error("finished BaseBranchDispatch should not be InFlight()")
	}
}

func TestBaseBranchRelease_FailingAndInFlight(t *testing.T) {
	rNoFailures := model.BaseBranchRelease{Failures: 0}
	if rNoFailures.Failing() {
		t.Error("BaseBranchRelease with 0 Failures should not be Failing()")
	}

	rFailing := model.BaseBranchRelease{Failures: 2}
	if !rFailing.Failing() {
		t.Error("BaseBranchRelease with >0 Failures should be Failing()")
	}

	rNoRun := model.BaseBranchRelease{}
	if rNoRun.InFlight() {
		t.Error("BaseBranchRelease with no RunID should not be InFlight()")
	}

	rInFlight := model.BaseBranchRelease{RunID: "run-1"}
	if !rInFlight.InFlight() {
		t.Error("BaseBranchRelease with RunID and empty Conclusion should be InFlight()")
	}

	rFinished := model.BaseBranchRelease{RunID: "run-1", Conclusion: "success"}
	if rFinished.InFlight() {
		t.Error("finished BaseBranchRelease should not be InFlight()")
	}
}

func TestBaseBranchRelease_Tags(t *testing.T) {
	repo := model.RepoRef{Owner: "owner", Name: "repo"}
	if tag := model.BaseReleaseTriageTag(repo, "main", "c0ffee"); tag != "base-release-triage:owner/repo:main:c0ffee" {
		t.Errorf("BaseReleaseTriageTag = %q, want %q", tag, "base-release-triage:owner/repo:main:c0ffee")
	}
	if tag := model.BaseReleaseTriageBranchTag(repo, "main"); tag != "base-release-triage:owner/repo:main" {
		t.Errorf("BaseReleaseTriageBranchTag = %q, want %q", tag, "base-release-triage:owner/repo:main")
	}
}

func TestBaseBranchStoreOperations(t *testing.T) {
	ctx := context.Background()
	st := openBaseBranchTestStore(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	now := time.Now().UTC().Truncate(time.Second)

	// 1. BaseBranchCI CRUD
	ciList, err := st.ListBaseBranchCI(ctx)
	if err != nil {
		t.Fatalf("ListBaseBranchCI: %v", err)
	}
	if len(ciList) != 0 {
		t.Errorf("expected empty BaseBranchCI list, got %d", len(ciList))
	}

	ci := model.BaseBranchCI{
		Repo:       repo,
		Branch:     "main",
		RunID:      "run-ci-1",
		Commit:     "commit1",
		StartedAt:  now,
		Conclusion: "success",
	}
	if err := st.PutBaseBranchCI(ctx, ci); err != nil {
		t.Fatalf("PutBaseBranchCI: %v", err)
	}

	gotCI, err := st.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatalf("GetBaseBranchCI: %v", err)
	}
	if gotCI.RunID != "run-ci-1" || gotCI.Commit != "commit1" {
		t.Errorf("GetBaseBranchCI = %+v", gotCI)
	}

	// 2. BaseBranchDispatch CRUD
	dList, err := st.ListBaseBranchDispatch(ctx)
	if err != nil {
		t.Fatalf("ListBaseBranchDispatch: %v", err)
	}
	if len(dList) != 0 {
		t.Errorf("expected empty BaseBranchDispatch list, got %d", len(dList))
	}

	dispatch := model.BaseBranchDispatch{
		Repo:       repo,
		Branch:     "main",
		Commit:     "commit1",
		Jobs:       []string{"build", "test"},
		RunID:      "run-disp-1",
		StartedAt:  now,
		Conclusion: "success",
	}
	if err := st.PutBaseBranchDispatch(ctx, dispatch); err != nil {
		t.Fatalf("PutBaseBranchDispatch: %v", err)
	}

	gotDispatch, err := st.GetBaseBranchDispatch(ctx, repo, "main")
	if err != nil {
		t.Fatalf("GetBaseBranchDispatch: %v", err)
	}
	if gotDispatch.RunID != "run-disp-1" || len(gotDispatch.Jobs) != 2 {
		t.Errorf("GetBaseBranchDispatch = %+v", gotDispatch)
	}

	// 3. BaseBranchRelease CRUD
	rList, err := st.ListBaseBranchRelease(ctx)
	if err != nil {
		t.Fatalf("ListBaseBranchRelease: %v", err)
	}
	if len(rList) != 0 {
		t.Errorf("expected empty BaseBranchRelease list, got %d", len(rList))
	}

	release := model.BaseBranchRelease{
		Repo:       repo,
		Branch:     "main",
		Commit:     "commit1",
		RunID:      "run-rel-1",
		StartedAt:  now,
		Conclusion: "success",
	}
	if err := st.PutBaseBranchRelease(ctx, release); err != nil {
		t.Fatalf("PutBaseBranchRelease: %v", err)
	}

	gotRelease, err := st.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil {
		t.Fatalf("GetBaseBranchRelease: %v", err)
	}
	if gotRelease.RunID != "run-rel-1" || gotRelease.Commit != "commit1" {
		t.Errorf("GetBaseBranchRelease = %+v", gotRelease)
	}
}
