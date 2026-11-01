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
	"testing"

	"github.com/ai-on-gke/grain/pkg/forkhead"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// -fork-pull-request's open pull request resolves from its label, with its
// head at refs/pull/41/head for a run to start from; the closed one's label
// resolves to nothing; and grain's own first pull request is still #9000.
func TestForkPullRequestSeedResolvesTheOpenOneOnly(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, bare, err := configureGitHubSimDemoWith(t.TempDir(), ui.Config{}, repo,
		func(sim *githubsim.Sim, bare string) error { return seedDemoForkPullRequests(sim, bare, repo) })
	if err != nil {
		t.Fatalf("configureGitHubSimDemoWith: %v", err)
	}
	r, err := forkhead.Resolve(cfg.GitHubForkPullRequests, repo, model.ForkHead{Owner: "contrib", Branch: "fix/readme-typo"})
	if err != nil {
		t.Fatalf("Resolve(open): %v", err)
	}
	if r.Ref.Number != 41 || r.Base != "main" || r.InRepo || r.Detail.HeadRepo != "contrib/widgets" {
		t.Fatalf("Resolve(open) = %+v", r)
	}
	if _, err := demoGit(bare, "rev-parse", "--verify", "refs/pull/41/head"); err != nil {
		t.Fatalf("refs/pull/41/head: %v", err)
	}
	if _, err := forkhead.Resolve(cfg.GitHubForkPullRequests, repo, model.ForkHead{Owner: "contrib", Branch: "feat/old-idea"}); err == nil {
		t.Fatal("Resolve(closed) found a pull request")
	}
	if _, err := demoGit(bare, "update-ref", "refs/heads/grain/task-1", "refs/pull/41/head"); err != nil {
		t.Fatal(err)
	}
	opened, err := cfg.GitHubForkPullRequests.(*github.RESTClient).CreatePullRequest(repo.Owner, repo.Name, "grain/task-1", "main", "t", "b")
	if err != nil || opened.Number != 9000 {
		t.Fatalf("CreatePullRequest = %+v, %v; want grain's first pull request to be #9000", opened, err)
	}
}
