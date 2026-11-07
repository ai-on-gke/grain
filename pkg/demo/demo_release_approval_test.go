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
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain demo -github-sim` walks the GitHub Actions release approval end
// to end (docs/test-runs/2026-11-04-release-approval.md, F2): a
// -pending tag in the sim raises release.pending on the demo's reconcile
// loop, a person's Approve dispatches promote-release.yml once through
// the sim, the dispatch is read back from the loopback sim hook, and a
// second press is refused. Before, the demo ran no release-pending
// reconciler, offered no forge a release could be dispatched through,
// and the sim had no workflow-dispatch endpoint.
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
	// repository and per process, and other tests here reconcile
	// acme/widgets.
	repo := model.RepoRef{Owner: "acme", Name: "gizmos"}
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
	srv := githubSimDemoHandler(ui.NewServerWithClient(ui.NewClient(cfg, store)), simClient)
	if _, err := demoGit(bare, "-c", "user.name=train", "-c", "user.email=train@example.invalid",
		"tag", "-a", "v2.5.0-pending", "-m", "v2.5.0 staged", "main"); err != nil {
		t.Fatal(err)
	}
	commit, err := demoGit(bare, "rev-parse", "main^{commit}")
	if err != nil {
		t.Fatal(err)
	}

	if err := demoReconcileCycle(ctx, store, simClient, time.Now().UTC()); err != nil {
		t.Fatalf("demoReconcileCycle: %v", err)
	}
	subject := model.NotificationSubject(model.FamilyReleasePending, "v2.5.0")
	if n, err := store.OpenNotification(ctx, subject); err != nil || n == nil || n.Repo != repo.String() {
		t.Fatalf("no open %s for acme/gizmos after the demo's reconcile saw v2.5.0-pending: %+v (err %v)", subject, n, err)
	}

	do := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:4321"
		req.Header.Set("Authorization", "Bearer "+code)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	approve := "/api/repos/acme/gizmos/pending-releases/v2.5.0/approve"
	if rec := do(http.MethodPost, approve); rec.Code != http.StatusAccepted {
		t.Fatalf("POST %s = %d: %s", approve, rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, approve); rec.Code != http.StatusBadRequest {
		t.Errorf("a second press = %d: %s, want 400 while the first dispatch's record is good", rec.Code, rec.Body)
	}
	rec := do(http.MethodGet, "/api/e2e/github-sim/repos/acme/gizmos/actions/workflows/promote-release.yml/dispatches")
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
}

func decodeB64(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding %q: %v", s, err)
	}
	return string(raw)
}
