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

package daemon

// GraphQL for a repository on a host only forgecli reaches: resolving a
// review thread names the thread's node ID and no repository, and went
// to -github-host (api.github.com) with the depot login on it -- a 401,
// and a thread the operator had to resolve by hand.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// openReviewedPullRequest opens a pull request for a fresh task's branch
// on depot and has a review bot leave one inline comment on it, whose
// thread is "thread-<commentID>".
func (f *depotFixture) openReviewedPullRequest(t *testing.T, client *github.RESTClient, taskID string, commentID int) (branch string, number int) {
	t.Helper()
	task := f.fileTask(t, taskID)
	f.pushBranch(t, task.BranchName())
	pr, err := client.CreatePullRequest(f.repo.Owner, f.repo.Name, task.BranchName(), "main", "t", "b")
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	f.depot.sim.ReviewComments[pr.Number] = append(f.depot.sim.ReviewComments[pr.Number],
		github.ReviewComment{ID: commentID, User: "review-bot[bot]", Body: "nit", Path: "work.txt"})
	return task.BranchName(), pr.Number
}

// assertGraphQLThroughForgeCLI checks depot saw at least one GraphQL run
// of forgecli, every one as `api graphql` -- the tool's own relative
// endpoint, which it posts to its GH_HOST's GraphQL -- under GH_HOST set
// to depot, and never one handed a whole URL.
func (f *depotFixture) assertGraphQLThroughForgeCLI(t *testing.T) {
	t.Helper()
	saw := false
	for _, c := range f.depot.apiCalls() {
		if strings.Contains(c, "api http") {
			t.Errorf("forgecli was handed a whole URL: %s", c)
		}
		if strings.HasPrefix(c, "api graphql ") {
			saw = true
			if c != "api graphql GH_HOST="+depotTestHost {
				t.Errorf("GraphQL run %q, want GH_HOST=%s", c, depotTestHost)
			}
		}
	}
	if !saw {
		t.Fatalf("depot saw no `forgecli api graphql`: %q", f.depot.apiCalls())
	}
}

// The orchestrator's resolve of a grain-relayed thread
// (resolveHandledRelayedThreads) calls ResolveReviewThread with the pull
// request's bare owner; a RepoRef's StoredOwner gives the host-qualified
// one. Both go to depot through `forgecli api graphql`.
func TestResolveReviewThreadOnAForgeCLIHostGoesThroughForgeCLIGraphQL(t *testing.T) {
	f := newDepotFixture(t)
	client, guard := f.client(t)
	_, number := f.openReviewedPullRequest(t, client, "t10", 501)
	f.depot.sim.ReviewComments[number] = append(f.depot.sim.ReviewComments[number],
		github.ReviewComment{ID: 502, User: "review-bot[bot]", Body: "nit 2", Path: "work.txt"})

	if err := client.ResolveReviewThread(f.repo.Owner, f.repo.Name, "thread-501"); err != nil {
		t.Fatalf("ResolveReviewThread(bare owner): %v\ndepot saw: %q", err, f.depot.requests())
	}
	if err := client.ResolveReviewThread(f.repo.StoredOwner(), f.repo.Name, "thread-502"); err != nil {
		t.Fatalf("ResolveReviewThread(host-qualified owner): %v\ndepot saw: %q", err, f.depot.requests())
	}
	resolved := f.depot.sim.ResolvedThreads["thread-501"] && f.depot.sim.ResolvedThreads["thread-502"]
	if !resolved {
		t.Fatalf("depot resolved %v", f.depot.sim.ResolvedThreads)
	}
	if len(guard.calls) != 0 {
		t.Fatalf("api.github.com was asked: %q", guard.calls)
	}
	f.assertGraphQLThroughForgeCLI(t)
}

// `grain pr resolve` -- the sandbox CLI's ResolveForTask, which runs the
// MCP resolve_review_thread tool for the task's own pull request -- reads
// the pull request's threads and resolves the one holding the comment,
// all on depot.
func TestGrainPRResolveOnAForgeCLIHostResolvesOnThatHost(t *testing.T) {
	f := newDepotFixture(t)
	client, guard := f.client(t)
	_, number := f.openReviewedPullRequest(t, client, "t11", 601)
	opener := &PullRequestOpener{Store: f.store, Client: client}

	text, refused, err := opener.ResolveForTask(f.ctx, "t11", "", 601)
	if err != nil || refused {
		t.Fatalf("ResolveForTask = %q, refused=%v, %v\ndepot saw: %q", text, refused, err, f.depot.requests())
	}
	if !strings.Contains(text, "Resolved thread thread-601") {
		t.Fatalf("ResolveForTask = %q", text)
	}
	resolved := f.depot.sim.ResolvedThreads["thread-601"]
	if !resolved {
		t.Fatalf("thread-601 on #%d was not resolved on depot", number)
	}
	if len(guard.calls) != 0 {
		t.Fatalf("api.github.com was asked: %q", guard.calls)
	}
	f.assertGraphQLThroughForgeCLI(t)
}

// A GraphQL document whose variables name a repository on one host, sent
// by a client that said it is about a repository on another, is refused
// before either host's transport is asked, naming both hosts.
func TestForgeRouterRefusesGraphQLBoundForAnotherHost(t *testing.T) {
	guard := &defaultHostGuard{}
	asked := false
	r := &forgeRouter{def: guard, defHost: "github.com",
		transportFor: func(host, owner, repo string) (github.Transport, error) {
			asked = true
			return guard, nil
		}}
	body := []byte(`{"query":"q","variables":{"owner":"acme","repo":"widgets","number":1}}`)
	headers := map[string]string{github.RepoHeader: "depot.example.com/experimental/grain",
		"Authorization": "token depot-login"}
	_, err := r.Request("POST", "/graphql", headers, body)
	if err == nil {
		t.Fatalf("a GraphQL call for a depot.example.com repository bound for github.com was sent")
	}
	for _, want := range []string{"github.com", "depot.example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if asked || len(guard.calls) != 0 {
		t.Fatalf("a transport was asked: transportFor=%v, default=%q", asked, guard.calls)
	}
}

// A node mutation names no repository in its variables, so the RepoHeader
// decides its host.
func TestForgeRouterRoutesANodeMutationByItsRepoHeader(t *testing.T) {
	r := &forgeRouter{defHost: "github.com"}
	body := []byte(`{"query":"mutation","variables":{"threadId":"T_1"}}`)
	host, owner, repo, _, newBody, ok := r.route("/graphql",
		map[string]string{github.RepoHeader: depotTestHost + "/experimental/grain"}, body)
	if !ok || host != depotTestHost || owner != "experimental" || repo != "grain" || string(newBody) != string(body) {
		t.Fatalf("route = %q %q %q %s %v", host, owner, repo, newBody, ok)
	}
	if _, _, _, _, _, ok := r.route("/graphql", nil, body); ok {
		t.Fatalf("a node mutation with no RepoHeader was routed")
	}
}
