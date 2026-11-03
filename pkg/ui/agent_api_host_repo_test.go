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

package ui_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestAgentAPIReachesAHostQualifiedRepo is a release manager on a
// repository off the default host starting a release run through the
// sandbox channel, however it spells the repository: the escaped
// <host>%2F<owner> segment the UI writes, the host/owner/name path an
// agent writes, or an SSH remote -- and a grant written one way matches a
// call written another.
func TestAgentAPIReachesAHostQualifiedRepo(t *testing.T) {
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, client, store := releaseServer(t, runner)
	ctx := context.Background()
	repo := model.RepoRef{Host: "git.forge.example", Owner: "experimental", Name: "grain"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"git.forge.example/experimental/grain"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "base-1", Commit: "abc123abc123abc",
		StartedAt: baseTime, Conclusion: string(ci.ConclusionCancelled), Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "1", "git.forge.example/experimental/grain", "api:release-manager")
	// A grant naming the repository as a URL, held by a task on another one.
	seedAPITask(t, client, "2", "acme/widgets", "api:ci:read@https://Git.Forge.Example/Experimental/grain.git")

	sandbox := func(id, method, path, body string) (int, string) {
		t.Helper()
		rec := doWithHeaders(t, srv, method, path, body, map[string]string{
			ui.ActingTaskHeader: id, ui.ChannelHeader: ui.SandboxCLIChannel, "Content-Type": "application/json",
		})
		return rec.Code, rec.Body.String()
	}
	for _, path := range []string{
		"/api/repos/git.forge.example%2Fexperimental/grain/release-runs",
		"/api/repos/git.forge.example/experimental/grain/release-runs",
		"/api/repos/git@git.forge.example:experimental/grain/release-runs",
	} {
		before := len(runner.started)
		code, body := sandbox("1", http.MethodPost, path, `{"branch":"main"}`)
		if code != http.StatusAccepted {
			t.Errorf("POST %s: %d %s, want 202", path, code, body)
			continue
		}
		if len(runner.started) != before+1 || !model.SameRepoText(runner.started[before].Repo, repo.String()) {
			t.Errorf("POST %s started %+v, want a run of %s", path, runner.started[before:], repo)
		}
	}

	for _, q := range []string{
		"git.forge.example/experimental/grain",
		"git@git.forge.example:experimental/grain",
		"https://git.forge.example/experimental/grain.git",
		"GIT.forge.example/Experimental/Grain",
	} {
		if code, body := sandbox("2", http.MethodGet, "/api/ci/runs?repo="+q, ""); code != http.StatusOK {
			t.Errorf("GET /api/ci/runs?repo=%s: %d %s, want 200", q, code, body)
		}
	}
	if code, body := sandbox("2", http.MethodGet, "/api/ci/runs?repo=acme/widgets", ""); code != http.StatusForbidden {
		t.Errorf("GET /api/ci/runs on a repository the grant does not name: %d %s, want 403", code, body)
	}
	if code, body := sandbox("2", http.MethodGet, "/api/ci/runs?repo=nope", ""); code != http.StatusBadRequest ||
		!strings.Contains(body, "owner/name") {
		t.Errorf("GET /api/ci/runs?repo=<not a repo>: %d %s, want 400", code, body)
	}
}
