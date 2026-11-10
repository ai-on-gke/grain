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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var demoReleaseApprovalSeq atomic.Uint64

// `grain demo -github-sim` walks the GitHub Actions release approval end
// to end (docs/test-runs/2026-11-04-release-approval.md, F2 and F5): a
// -pending tag in the sim raises release.pending on the demo's reconcile
// loop, a person's Approve dispatches promote-release.yml once through
// the sim, the dispatch is read back from the loopback sim hook, a
// second press is refused, and a release approved from the CLI/API before
// the watcher polls neither raises release.pending on the next poll nor
// dispatches a second time past the 15-minute record lifetime.
func TestDemoGitHubSimApprovesAPendingReleaseThroughTheSim(t *testing.T) {
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
	// A repository of its own: the release watch's poll interval is per
	// repository and per process, and other tests (or -count=2) reconcile
	// acme/widgets.
	repo := model.RepoRef{Owner: "acme", Name: fmt.Sprintf("gizmos-%d", demoReleaseApprovalSeq.Add(1))}
	cfg := ui.Config{
		Actor:              ui.DefaultActor("operator"),
		DefaultTarget:      &repo,
		ReleaseApprovalDir: dir,
		GitHubCalls:        newDemoGitHubCalls(),
		GitHubPullRequests: demoPullRequests{repo: repo, store: store},
	}
	cfg, bare, err := configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := store.PutConfig(ctx, model.Config{TargetRepos: []string{repo.String()}}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	// Left on every default -- no repo_config row at all -- which the repo
	// page already reports as ciMode: github, and which the release watch
	// must poll without requiring an explicit ciMode write (F1b).
	const code = "demo-release-approval-code"
	cfg.LocalAPIToken = code
	simClient := cfg.GitHubBranches.(github.Client)
	now := time.Now().UTC()
	uic := ui.NewClient(cfg, store)
	uic.Now = func() time.Time { return now }
	srv := githubSimDemoHandler(ui.NewServerWithClient(uic), simClient)
	if _, err := demoGit(bare, "-c", "user.name=train", "-c", "user.email=train@example.invalid",
		"tag", "-a", "v2.5.0-pending", "-m", "v2.5.0 staged", "main"); err != nil {
		t.Fatal(err)
	}
	commit, err := demoGit(bare, "rev-parse", "main^{commit}")
	if err != nil {
		t.Fatal(err)
	}

	if err := demoReconcileCycle(ctx, store, simClient, now); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	subject := model.NotificationSubject(model.FamilyReleasePending, "v2.5.0")
	if n, err := store.OpenNotification(ctx, subject); err != nil || n == nil || n.Repo != repo.String() {
		t.Fatalf("no open %s for %s after the demo's reconcile saw v2.5.0-pending: %+v (err %v)", subject, repo, n, err)
	}

	doBody := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:4321"
		req.Header.Set("Authorization", "Bearer "+code)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	do := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		return doBody(method, path, `{}`)
	}
	approve := "/api/repos/" + repo.String() + "/pending-releases/v2.5.0/approve"
	if rec := do(http.MethodPost, approve); rec.Code != http.StatusAccepted {
		t.Fatalf("POST %s = %d: %s", approve, rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, approve); rec.Code != http.StatusBadRequest {
		t.Errorf("a second press = %d: %s, want 400 while the first dispatch's record is good", rec.Code, rec.Body)
	}
	dispatchesPath := "/api/e2e/github-sim/repos/" + repo.String() + "/actions/workflows/promote-release.yml/dispatches"
	rec := do(http.MethodGet, dispatchesPath)
	var dispatches []githubsim.WorkflowDispatch
	if err := json.Unmarshal(rec.Body.Bytes(), &dispatches); err != nil {
		t.Fatalf("reading the sim's dispatches: %v (%d %s)", err, rec.Code, rec.Body)
	}
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %+v, want exactly one", dispatches)
	}
	d := dispatches[0]
	if d.Ref != "main" || d.Inputs["version"] != "v2.5.0" || d.Inputs["decision"] != "approve" ||
		d.Inputs["approval"] == "" || d.Inputs["approval_signature"] == "" {
		t.Errorf("dispatch = %+v, want v2.5.0 approve on main with a signed record", d)
	}
	if !strings.Contains(decodeB64(t, d.Inputs["approval"]), "commit: "+commit+"\n") {
		t.Errorf("the record does not bind v2.5.0-pending's commit %s:\n%s", commit, decodeB64(t, d.Inputs["approval"]))
	}

	// F5 repro: tag v2.6.0-pending, approve immediately (before the next
	// watcher poll), then run the next poll and try approving again after 15m.
	if _, err := demoGit(bare, "-c", "user.name=train", "-c", "user.email=train@example.invalid",
		"tag", "-a", "v2.6.0-pending", "-m", "v2.6.0 staged", "main"); err != nil {
		t.Fatal(err)
	}
	approve260 := "/api/repos/" + repo.String() + "/pending-releases/v2.6.0/approve"
	if rec := doBody(http.MethodPost, approve260, `{"allow_breaking":true}`); rec.Code != http.StatusAccepted {
		t.Fatalf("POST %s = %d: %s", approve260, rec.Code, rec.Body)
	}
	now = now.Add(11 * time.Minute)
	if err := demoReconcileCycle(ctx, store, simClient, now); err != nil {
		t.Fatalf("demoReconcileCycle after CLI approve: %v", err)
	}
	if n, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyReleasePending, "v2.6.0")); err != nil || n != nil {
		t.Fatalf("watcher raised release.pending:v2.6.0 after pre-poll approve: %+v (err %v)", n, err)
	}
	now = now.Add(10 * time.Minute) // 21m past the approval (> 15m)
	if rec := doBody(http.MethodPost, approve260, `{"allow_breaking":true}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "v2.6.0 was already answered") {
		t.Errorf("POST %s after 21m = %d: %s, want 400 saying v2.6.0 was already answered", approve260, rec.Code, rec.Body)
	}
	rec = do(http.MethodGet, dispatchesPath)
	if err := json.Unmarshal(rec.Body.Bytes(), &dispatches); err != nil {
		t.Fatalf("reading the sim's dispatches: %v", err)
	}
	if len(dispatches) != 2 {
		t.Fatalf("dispatches = %+v, want 2 (v2.5.0 and v2.6.0 once each)", dispatches)
	}
}

func decodeB64(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding %q: %v", s, err)
	}
	return string(raw)
}

// A promote-release.yml run that fails after Approve, through the sim
// (docs/test-runs/2026-11-10-release-train.md §5's follow-up): the run the
// dispatch queued is finished with conclusion failure and
// verify-release-approval's refusal (POST .../actions/runs/{id}/complete),
// and the next reconcile within the 5s active poll marks the decision
// failed and raises release.pending again, critical, with the run, the
// reason and the GET /api/release-approval-key remedy; a retry then
// dispatches again at once.
func TestDemoGitHubSimRaisesAPromoteRunThatFailsAfterApproveAgain(t *testing.T) {
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
	repo := model.RepoRef{Owner: "acme", Name: fmt.Sprintf("gizmos-%d", demoReleaseApprovalSeq.Add(1))}
	cfg := ui.Config{
		Actor:              ui.DefaultActor("operator"),
		DefaultTarget:      &repo,
		ReleaseApprovalDir: dir,
		GitHubCalls:        newDemoGitHubCalls(),
		GitHubPullRequests: demoPullRequests{repo: repo, store: store},
	}
	cfg, bare, err := configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := store.PutConfig(ctx, model.Config{TargetRepos: []string{repo.String()}}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	const code = "demo-release-dispatch-failure-code"
	cfg.LocalAPIToken = code
	simClient := cfg.GitHubBranches.(github.Client)
	now := time.Now().UTC()
	uic := ui.NewClient(cfg, store)
	uic.Now = func() time.Time { return now }
	srv := githubSimDemoHandler(ui.NewServerWithClient(uic), simClient)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:4321"
		req.Header.Set("Authorization", "Bearer "+code)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if _, err := demoGit(bare, "-c", "user.name=train", "-c", "user.email=train@example.invalid",
		"tag", "-a", "v2.7.0-pending", "-m", "v2.7.0 staged", "main"); err != nil {
		t.Fatal(err)
	}
	if err := demoReconcileCycle(ctx, store, simClient, now); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	subject := model.NotificationSubject(model.FamilyReleasePending, "v2.7.0")
	if n, err := store.OpenNotification(ctx, subject); err != nil || n == nil {
		t.Fatalf("no open %s: %+v (err %v)", subject, n, err)
	}

	approve := "/api/repos/" + repo.String() + "/pending-releases/v2.7.0/approve"
	if rec := do(http.MethodPost, approve, `{}`); rec.Code != http.StatusAccepted {
		t.Fatalf("POST %s = %d: %s", approve, rec.Code, rec.Body)
	}
	dispatches := func() []githubsim.WorkflowDispatch {
		t.Helper()
		rec := do(http.MethodGet, "/api/e2e/github-sim/repos/"+repo.String()+"/actions/workflows/promote-release.yml/dispatches", "")
		var out []githubsim.WorkflowDispatch
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("reading the sim's dispatches: %v (%d %s)", err, rec.Code, rec.Body)
		}
		return out
	}
	sent := dispatches()
	if len(sent) != 1 || sent[0].RunID == 0 {
		t.Fatalf("dispatches = %+v, want one with its run", sent)
	}
	runID := sent[0].RunID

	// Queued, the run raises nothing on the next poll.
	now = now.Add(6 * time.Second)
	if err := demoReconcileCycle(ctx, store, simClient, now); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("raised again while the run is queued: %+v", n)
	}

	complete := fmt.Sprintf("/api/e2e/github-sim/repos/%s/actions/runs/%d/complete", repo, runID)
	refusal := "verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set"
	body := fmt.Sprintf(`{"conclusion":"failure","jobs":[{"name":"approve the pending release","log":"##[error]%s\n"}]}`, refusal)
	if rec := do(http.MethodPost, complete, body); rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", complete, rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, complete, body); rec.Code != http.StatusConflict {
		t.Errorf("completing it twice = %d: %s, want 409", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, fmt.Sprintf("/api/e2e/github-sim/repos/%s/actions/runs/1/complete", repo), body); rec.Code != http.StatusNotFound {
		t.Errorf("completing no run = %d: %s, want 404", rec.Code, rec.Body)
	}

	now = now.Add(6 * time.Second)
	if err := demoReconcileCycle(ctx, store, simClient, now); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	commit, err := demoGit(bare, "rev-parse", "main^{commit}")
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.GetReleaseApprovalDecision(ctx, repo, "v2.7.0", commit)
	if err != nil || d == nil || !d.Failed() {
		t.Fatalf("decision after the failed run = %+v (err %v), want it marked failed", d, err)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("release.pending not raised again after the failed run: %+v (err %v)", n, err)
	}
	runRef := fmt.Sprintf("gha:%s:%s:%d", repo.Owner, repo.Name, runID)
	if n.Severity != model.NotificationCritical || n.Title != "v2.7.0 is ready to promote (approve dispatch failed)" ||
		!strings.HasPrefix(n.Body, "DISPATCH FAILED ("+runRef+"): "+refusal) ||
		!strings.Contains(n.Body, "GET /api/release-approval-key") {
		t.Fatalf("raised again as %s %q:\n%s", n.Severity, n.Title, n.Body)
	}

	// The retry dispatches again now, not 24 hours on.
	if rec := do(http.MethodPost, approve, `{}`); rec.Code != http.StatusAccepted {
		t.Fatalf("retry POST %s = %d: %s", approve, rec.Code, rec.Body)
	}
	if sent := dispatches(); len(sent) != 2 || sent[1].RunID == runID {
		t.Fatalf("dispatches after the retry = %+v, want a second run", sent)
	}
}
