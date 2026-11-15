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

// Moving a repository between git connections, on the default host and a
// second one, and what GET /api/deployment/reachable-repos says before
// and after (grain/task-4288, building on grain/task-4249's lifecycle
// rig).

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// pins is every repository-scoped connection, as "host scope=credential"
// with an empty host for the default one.
func (r *lifecycleRig) pins() []string {
	r.t.Helper()
	var out []string
	for _, v := range r.connections() {
		if strings.Contains(v.Scope, "/") {
			host := v.Host
			if host == "github.com" {
				host = ""
			}
			out = append(out, host+" "+v.Scope+"="+v.Credential)
		}
	}
	slices.Sort(out)
	return out
}

func (r *lifecycleRig) reachable() ui.ReachableReposResult {
	r.t.Helper()
	var res ui.ReachableReposResult
	if err := json.Unmarshal([]byte(r.must(http.MethodGet, "/api/deployment/reachable-repos", "")), &res); err != nil {
		r.t.Fatal(err)
	}
	return res
}

func reachableNames(res ui.ReachableReposResult) []string {
	var out []string
	for _, repo := range res.Repos {
		out = append(out, repo.Repo)
	}
	return out
}

func TestMovingARepositoryBetweenConnectionsOnTwoHosts(t *testing.T) {
	r := newLifecycleRig(t)
	dotcom := githubsim.New("acme", "widgets", "", "main")
	dotcom.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_acme":    {{FullName: "acme/widgets", Push: true}},
		"ghp_default": {{FullName: "acme/widgets"}, {FullName: "acme/docs"}},
	}
	ghe := githubsim.New("acme", "widgets", "", "main")
	ghe.Reachable = map[string][]githubsim.ReachableRepo{
		"ghp_ghe": {{FullName: "acme/widgets", Push: true}},
	}
	r.client.Config.GitReach = &gitproxy.Reach{
		Credentials: r.creds, API: dotcom, Host: "github.com",
		Transports: map[string]github.Transport{"ghe.example.com": ghe},
	}

	owner := r.addConnection("github.com", "acme", "ghp_acme")
	dflt := r.addConnection("github.com", "", "ghp_default")
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	r.must(http.MethodPost, "/api/repos", `{"repo":"ghe.example.com/acme/widgets"}`)

	before := r.reachable()
	if !before.Available || !slices.Equal(reachableNames(before), []string{"acme/docs", "acme/widgets"}) {
		t.Fatalf("reachable before the second host's connection = %+v", before)
	}
	if !slices.ContainsFunc(before.Repos, func(rr gitproxy.ReachableRepo) bool { return rr.Repo == "acme/widgets" && rr.Push }) {
		t.Fatalf("acme/widgets should be pushable through %s: %+v", owner, before.Repos)
	}

	// Pin to the unscoped connection, then move to the owner-scoped one:
	// one pin, naming the new credential, never two.
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+dflt+`"}`)
	if got := r.pins(); !slices.Equal(got, []string{" acme/widgets=" + dflt}) {
		t.Fatalf("pins after pinning to %s = %v", dflt, got)
	}
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+owner+`"}`)
	if got := r.pins(); !slices.Equal(got, []string{" acme/widgets=" + owner}) {
		t.Fatalf("pins after moving to %s = %v", owner, got)
	}

	// The second host's connection: it is not a connection on github.com,
	// so pinning the default host's repository to it is refused by name.
	gheCred := r.addConnection("ghe.example.com", "", "ghp_ghe")
	code, out := r.call(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+gheCred+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, gheCred) || !strings.Contains(out, "github.com") {
		t.Fatalf("pinning a github.com repository to %s's connection = %d %s, want a 400 naming both", "ghe.example.com", code, out)
	}
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection",
		`{"credential":"`+gheCred+`","host":"ghe.example.com"}`)
	if got := r.pins(); !slices.Equal(got, []string{" acme/widgets=" + owner, "ghe.example.com acme/widgets=" + gheCred}) {
		t.Fatalf("the same owner/name pinned on two hosts = %v, want one pin on each", got)
	}

	after := r.reachable()
	if want := []string{"acme/docs", "acme/widgets", "ghe.example.com/acme/widgets"}; !slices.Equal(reachableNames(after), want) {
		t.Fatalf("reachable with both hosts' connections = %v, want %v", reachableNames(after), want)
	}

	// Back to Default on the second host; then its connection can go, and
	// reachable-repos stops listing that host's repository.
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"","host":"ghe.example.com"}`)
	if got := r.pins(); !slices.Equal(got, []string{" acme/widgets=" + owner}) {
		t.Fatalf("pins after unpinning the second host = %v", got)
	}
	r.must(http.MethodDelete, "/api/deployment/git-connections/"+gheCred, "")
	if got := reachableNames(r.reachable()); !slices.Equal(got, []string{"acme/docs", "acme/widgets"}) {
		t.Fatalf("reachable after removing the second host's connection = %v", got)
	}

	// The unscoped connection the repository moved away from is no longer
	// named by it, so it can go; the pin it moved to stays.
	r.must(http.MethodDelete, "/api/deployment/git-connections/"+dflt, "")
	if got := r.pins(); !slices.Equal(got, []string{" acme/widgets=" + owner}) {
		t.Fatalf("pins after removing the connection moved away from = %v", got)
	}
	if got := reachableNames(r.reachable()); !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("reachable with only the owner connection = %v", got)
	}
}
