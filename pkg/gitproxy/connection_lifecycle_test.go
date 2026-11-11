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

package gitproxy

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Scenarios 1 and 4 of grain/task-4249, at the proxy's own layer: a
// connection removed from the deployment stops resolving for its
// repositories at once, and one re-added under the same name with a new
// token serves the new token -- no restart, and no cached credential
// outliving the file it was read from.
func TestARemovedConnectionStopsResolvingAndAReAddedOneServesItsNewToken(t *testing.T) {
	creds, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const name = "gc-lifecycle"
	var connected atomic.Bool
	creds.SetConnections(func(owner, repo string) (string, bool) {
		if connected.Load() && owner == "acme" {
			return name, true
		}
		return "", false
	})
	token := func() (string, bool) {
		t.Helper()
		got, ok := creds.Select("acme", "widgets")
		if !ok || got.Token == nil {
			return "", false
		}
		return *got.Token, true
	}

	if err := creds.SetPAT(name, "ghp_first"); err != nil {
		t.Fatal(err)
	}
	connected.Store(true)
	if got, ok := token(); !ok || got != "ghp_first" {
		t.Fatalf("connected repository resolves %q, %v; want ghp_first", got, ok)
	}

	// Removal, as DeleteTeamGitConnection does it: the connection list
	// first, then the credential.
	connected.Store(false)
	if err := creds.Remove(name); err != nil {
		t.Fatal(err)
	}
	if got, ok := token(); ok && got == "ghp_first" {
		t.Fatal("a removed connection's token still resolves for its repository")
	}
	if _, ok := creds.Get(name); ok {
		t.Fatal("a removed credential is still readable by name")
	}

	if err := creds.SetPAT(name, "ghp_second"); err != nil {
		t.Fatal(err)
	}
	connected.Store(true)
	if got, ok := token(); !ok || got != "ghp_second" {
		t.Fatalf("re-added connection resolves %q, %v; want the new token ghp_second", got, ok)
	}
}

// listedScope is a ScopeLookup and DeploymentRepoLister: one live run
// targeting acme/widgets, reading acme/docs and writing acme/site, on a
// deployment whose repository list is whatever list holds.
type listedScope struct{ list []string }

func (l *listedScope) GitScope(context.Context, string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error) {
	return &model.RepoRef{Owner: "acme", Name: "widgets"},
		[]model.RepoRef{{Owner: "acme", Name: "docs"}},
		[]model.RepoRef{{Owner: "acme", Name: "site"}}, nil
}

func (l *listedScope) TargetRepos(context.Context) ([]string, error) { return l.list, nil }

// Scenario 3 of grain/task-4249 at the authorizer (grain/task-4334): a
// run dispatched before its repository was removed may no longer clone
// or push it, nor push a write target removed alongside it, and is told
// why; what it reads is still its task's scope, which the list has never
// governed.
func TestARunMayNotReachARepositoryRemovedFromTheDeploymentWhileItWasLive(t *testing.T) {
	ctx := context.Background()
	scope := &listedScope{list: []string{"acme/widgets", "acme/site"}}
	a := &ModelAuthorizer{Store: scope}
	may := func(repo, action string) bool {
		t.Helper()
		ok, err := a.Authorize(ctx, "sb", "acme", repo, action)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, repo := range []string{"widgets", "site"} {
		if !may(repo, "git-upload-pack") || !may(repo, "git-receive-pack") {
			t.Fatalf("acme/%s refused while it is on the list", repo)
		}
	}

	scope.list = []string{"acme/gadgets"}
	for _, repo := range []string{"widgets", "site"} {
		for _, action := range []string{"info/refs", "git-upload-pack", "git-receive-pack"} {
			if may(repo, action) {
				t.Errorf("acme/%s %s allowed after it was removed from the deployment", repo, action)
			}
		}
		reason, removed := a.RemovedReason(ctx, "sb", "acme", repo)
		if !removed || !strings.Contains(reason, "acme/"+repo) || !strings.Contains(reason, "no longer on this deployment") ||
			!strings.Contains(reason, "`acme/gadgets`") {
			t.Errorf("RemovedReason(acme/%s) = %q, %v; want it to name the repo, the removal and the list", repo, reason, removed)
		}
	}
	if !may("docs", "git-upload-pack") {
		t.Error("a read the task was granted was refused because the repository is not on the list")
	}
	if _, removed := a.RemovedReason(ctx, "sb", "acme", "docs"); removed {
		t.Error("a read-only repository was explained as removed")
	}

	// An empty list is an unrestricted deployment (model.RepoAllowed).
	scope.list = nil
	if !may("widgets", "git-receive-pack") {
		t.Error("an unrestricted deployment refused the run's own target")
	}
	// A store that cannot list the deployment's repositories leaves the
	// run's scope as the whole answer.
	if ok, err := (&ModelAuthorizer{Store: scopeOnly{scope}}).Authorize(ctx, "sb", "acme", "widgets", "git-receive-pack"); err != nil || !ok {
		t.Errorf("a scope-only store refused the run's own target: %v, %v", ok, err)
	}
}

type scopeOnly struct{ s ScopeLookup }

func (s scopeOnly) GitScope(ctx context.Context, sandbox string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error) {
	return s.s.GitScope(ctx, sandbox)
}
