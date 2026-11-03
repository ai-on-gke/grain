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

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestQualificationDemoWithGitHubSimSeedsBranchesAndPromotes(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemo(dir, ui.Config{Actor: ui.DefaultActor("operator"), DefaultTarget: &repo}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := seedQualificationDemo(ctx, store, repo, bare); err != nil {
		t.Fatalf("seedQualificationDemo: %v", err)
	}

	rel, err := store.GetRelease(ctx, repo, demoQualificationRelease)
	if err != nil || rel == nil {
		t.Fatalf("GetRelease(%s) = %v, %v", demoQualificationRelease, rel, err)
	}
	if rel.Status != model.ReleaseActive {
		t.Fatalf("release status = %s, want %s", rel.Status, model.ReleaseActive)
	}
	for _, branch := range []string{"1.0.latest", "1.0.rc.1"} {
		if _, err := demoGit(bare, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
			t.Fatalf("simulated GitHub has no %s: %v", branch, err)
		}
	}

	if _, err := store.PromoteCandidate(ctx, repo, demoQualificationRelease); err != nil {
		t.Fatalf("PromoteCandidate: %v", err)
	}
	releases, ok := cfg.GitHubBranches.(github.Client)
	if !ok {
		t.Fatalf("-github-sim wired a %T, want github.Client", cfg.GitHubBranches)
	}
	if err := demoReconcileCycle(ctx, store, releases, time.Now().UTC()); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}

	cand, err := store.CurrentCandidateForRelease(ctx, rel.ID)
	if err != nil || cand == nil {
		t.Fatalf("CurrentCandidateForRelease = %v, %v", cand, err)
	}
	if cand.Status != model.CandidatePromoted || cand.LastError != "" {
		t.Fatalf("candidate = %s (error %q), want %s with no error", cand.Status, cand.LastError, model.CandidatePromoted)
	}
	if _, err := demoGit(bare, "rev-parse", "--verify", "refs/heads/1.0"); err != nil {
		t.Fatalf("simulated GitHub has no promoted 1.0 branch: %v", err)
	}
}

func TestSeedDemoMergeBatchLinksToFakeCIRun(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	storedCfg := model.DefaultConfig()
	storedCfg.TargetRepos = []string{repo.String()}
	storedCfg.GitConnections = []model.TeamGitConnection{{Credential: "default"}}
	if err := store.PutConfig(ctx, storedCfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	cfg, err := configureFakeCIDemo(ctx, store, ui.Config{
		Actor:         ui.DefaultActor("operator"),
		DefaultTarget: &repo,
		Capabilities:  append(ui.OfferedCapabilities(), ui.APICapabilities()...),
	}, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}

	srv := ui.NewServer(withAccessCode(cfg), store)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:40000"
		presentAccessCode(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/api/merge-batches?repo=acme/widgets")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/merge-batches: %d %s", rec.Code, rec.Body)
	}
	var batches ui.MergeBatches
	if err := json.Unmarshal(rec.Body.Bytes(), &batches); err != nil {
		t.Fatalf("decoding merge batches: %v", err)
	}
	if len(batches.Batches) != 1 || batches.Batches[0].RunID == "" {
		t.Fatalf("batches = %+v, want 1 batch with non-empty RunID", batches.Batches)
	}

	runRec := get("/api/ci/runs/" + batches.Batches[0].RunID)
	if runRec.Code != http.StatusOK {
		t.Fatalf("GET /api/ci/runs/%s: %d %s", batches.Batches[0].RunID, runRec.Code, runRec.Body)
	}
	var run struct {
		ID     string `json:"id"`
		Repo   string `json:"repo"`
		Ref    string `json:"ref"`
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(runRec.Body.Bytes(), &run); err != nil {
		t.Fatalf("decoding CI run: %v", err)
	}
	if run.ID != batches.Batches[0].RunID || run.Repo != repo.String() ||
		run.Ref != batches.Batches[0].Ref || run.Commit != batches.Batches[0].Commit {
		t.Fatalf("run = %+v, want match for batch %+v", run, batches.Batches[0])
	}
}
