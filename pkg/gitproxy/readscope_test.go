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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ciScoped is a ReadOnlyScopeAuthorizer over a model that knows nothing
// about any sandbox -- which is exactly what a CI job's sandbox meets in
// a real deployment, since there is no task and so no run behind it.
func ciScoped(scopes *ReadScopes, forbidden *ForbiddenSet) *ReadOnlyScopeAuthorizer {
	return &ReadOnlyScopeAuthorizer{
		Scopes:    scopes,
		Next:      &ModelAuthorizer{Store: stubScopeLookup{}, Forbidden: forbidden},
		Forbidden: forbidden,
	}
}

func allows(t *testing.T, a Authorizer, sandbox, owner, repo, action string) bool {
	t.Helper()
	ok, err := a.Authorize(context.Background(), sandbox, owner, repo, action)
	if err != nil {
		t.Fatalf("authorizing %s %s/%s for %s: %v", action, owner, repo, sandbox, err)
	}
	return ok
}

func TestAReadScopeAllowsFetchingTheOneRepoUnderTest(t *testing.T) {
	scopes := NewReadScopes()
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	a := ciScoped(scopes, nil)

	for _, action := range []string{"info/refs", "git-upload-pack"} {
		if !allows(t, a, "ci-1", "acme", "widgets", action) {
			t.Errorf("%q against the repo under test should be allowed", action)
		}
	}
}

func TestAReadScopeNeverAllowsAPush(t *testing.T) {
	scopes := NewReadScopes()
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	if allows(t, ciScoped(scopes, nil), "ci-1", "acme", "widgets", "git-receive-pack") {
		t.Error("a CI sandbox must not be able to push the repo it is testing")
	}
}

func TestAReadScopeSupportsMultipleRepositories(t *testing.T) {
	scopes := NewReadScopes()
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	if allows(t, ciScoped(scopes, nil), "ci-1", "acme", "other", "git-upload-pack") {
		t.Error("a CI sandbox must not be able to fetch an ungranted second repository")
	}
	// Granting again adds rather than replaces: a sandbox may be granted
	// read-only access to additional repositories (e.g. cross-repo read CI capability).
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "other"})
	if !allows(t, ciScoped(scopes, nil), "ci-1", "acme", "widgets", "git-upload-pack") {
		t.Error("the primary repository should remain allowed after granting a second repository")
	}
	if !allows(t, ciScoped(scopes, nil), "ci-1", "acme", "other", "git-upload-pack") {
		t.Error("the second granted repository should also be allowed")
	}
	if allows(t, ciScoped(scopes, nil), "ci-1", "acme", "third", "git-upload-pack") {
		t.Error("an ungranted third repository must not be allowed")
	}
	if allows(t, ciScoped(scopes, nil), "ci-1", "acme", "other", "git-receive-pack") {
		t.Error("pushing to any granted repository must never be allowed")
	}
}

func TestAReadScopeIsGoneOnceDropped(t *testing.T) {
	scopes := NewReadScopes()
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	scopes.Drop("ci-1")
	if scopes.Len() != 0 {
		t.Fatalf("Len = %d after the only grant was dropped", scopes.Len())
	}
	if allows(t, ciScoped(scopes, nil), "ci-1", "acme", "widgets", "git-upload-pack") {
		t.Error("a released CI sandbox must authorize nothing")
	}
	// Dropping what is not there is what a retried cleanup does.
	scopes.Drop("ci-1")
}

func TestAReadScopeIsCaseAndDotGitInsensitive(t *testing.T) {
	scopes := NewReadScopes()
	scopes.Grant("ci-1", model.RepoRef{Owner: "Acme", Name: "Widgets"})
	if !allows(t, ciScoped(scopes, nil), "ci-1", "acme", "widgets.git", "git-upload-pack") {
		t.Error("expected the same case/suffix-insensitive match every other repo comparison here makes")
	}
}

func TestAReadScopeBelongsToOneSandbox(t *testing.T) {
	scopes := NewReadScopes()
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	if allows(t, ciScoped(scopes, nil), "ci-2", "acme", "widgets", "git-upload-pack") {
		t.Error("another sandbox must not inherit this job's scope")
	}
}

func TestAReadScopeDoesNotReachAForbiddenRepository(t *testing.T) {
	forbidden := NewForbiddenSet(model.RepoRef{Owner: "acme", Name: "grain-state"})
	scopes := NewReadScopes()
	// The registry says yes; the forbidden set has to win anyway.
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "grain-state"})
	a := ciScoped(scopes, forbidden)
	if allows(t, a, "ci-1", "acme", "grain-state", "git-upload-pack") {
		t.Error("a read scope must not get a CI job into the repository no sandbox may reach")
	}
	if reason, refused := a.Refusal("acme", "grain-state"); !refused || reason != ForbiddenReason {
		t.Errorf("Refusal(%q) = %q, %v; want the forbidden explanation passed through", "acme/grain-state", reason, refused)
	}
}

func TestAnUnscopedSandboxStillFallsThroughToTheModel(t *testing.T) {
	scopes := NewReadScopes()
	a := &ReadOnlyScopeAuthorizer{
		Scopes: scopes,
		Next: &ModelAuthorizer{Store: stubScopeLookup{
			target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		}},
	}
	if !allows(t, a, "sandbox-0", "acme", "widgets", "git-receive-pack") {
		t.Error("an agent run's sandbox should still be authorized by its task, push included")
	}
	if allows(t, a, "sandbox-0", "acme", "other", "git-upload-pack") {
		t.Error("the wrapper must not widen what the model allows")
	}
}

func TestANilReadScopesGrantsNothing(t *testing.T) {
	var scopes *ReadScopes
	if scopes.Len() != 0 {
		t.Error("a nil registry should read as empty")
	}
	if _, ok := scopes.Repo("ci-1"); ok {
		t.Error("a nil registry should hold no grant")
	}
	scopes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	scopes.Drop("ci-1")
	if allows(t, ciScoped(scopes, nil), "ci-1", "acme", "widgets", "git-upload-pack") {
		t.Error("a nil registry must authorize nothing")
	}
}

func TestAWriteScopeAllowsPushAndFetch(t *testing.T) {
	writes := NewWriteScopes()
	writes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	a := &ReadOnlyScopeAuthorizer{
		WriteScopes: writes,
		Next:        &ModelAuthorizer{Store: stubScopeLookup{}},
	}

	for _, action := range []string{"info/refs", "git-upload-pack", "git-receive-pack"} {
		if !allows(t, a, "ci-1", "acme", "widgets", action) {
			t.Errorf("%q against the repo with write scope should be allowed", action)
		}
	}
	if allows(t, a, "ci-1", "acme", "other", "git-receive-pack") {
		t.Error("pushing to an ungranted repository must not be allowed")
	}
	if allows(t, a, "ci-2", "acme", "widgets", "git-receive-pack") {
		t.Error("another sandbox must not inherit this job's write scope")
	}

	writes.Drop("ci-1")
	if writes.Len() != 0 {
		t.Fatalf("Len = %d after drop", writes.Len())
	}
	if allows(t, a, "ci-1", "acme", "widgets", "git-receive-pack") {
		t.Error("dropped write scope must authorize nothing")
	}
}

func TestAWriteScopeDoesNotReachAForbiddenRepository(t *testing.T) {
	forbidden := NewForbiddenSet(model.RepoRef{Owner: "acme", Name: "grain-state"})
	writes := NewWriteScopes()
	writes.Grant("ci-1", model.RepoRef{Owner: "acme", Name: "grain-state"})
	a := &ReadOnlyScopeAuthorizer{
		WriteScopes: writes,
		Next:        &ModelAuthorizer{Store: stubScopeLookup{}, Forbidden: forbidden},
		Forbidden:   forbidden,
	}
	if allows(t, a, "ci-1", "acme", "grain-state", "git-receive-pack") {
		t.Error("a write scope must not get a CI job into the repository no sandbox may reach")
	}
}
