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

package orchestrator

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestLinksFollowTheRepoHost: a pull request on a repository off the
// default host is linked on that host, never under github.com.
func TestLinksFollowTheRepoHost(t *testing.T) {
	pr := model.PullRequestRef{Repo: model.RepoRef{Host: "depot.code.corp.goog", Owner: "experimental", Name: "grain"}, Number: 26}
	const want = "https://depot.code.corp.goog/experimental/grain/pull/26"

	if got, w := statusWithLink(model.TreeStatusPROpen, &pr), model.TreeStatusPROpen+" [depot.code.corp.goog/experimental/grain#26]("+want+")"; got != w {
		t.Errorf("statusWithLink = %q, want %q", got, w)
	}
	if got := reviewPage(pr, ""); got != want+"/files" {
		t.Errorf("reviewPage = %q, want %q", got, want+"/files")
	}
	if got := repoWebURL("", pr.Repo); got != "https://depot.code.corp.goog/experimental/grain" {
		t.Errorf("repoWebURL = %q", got)
	}

	github := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 42}
	if got := reviewPage(github, ""); got != "https://github.com/acme/widgets/pull/42/files" {
		t.Errorf("reviewPage(github) = %q", got)
	}
	if got := originLink(model.GitHubOrigin(github.Repo, 7)); got != "[acme/widgets#7](https://github.com/acme/widgets/issues/7)" {
		t.Errorf("originLink = %q", got)
	}
}
