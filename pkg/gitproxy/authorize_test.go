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
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type stubScopeLookup struct {
	target *model.RepoRef
	reads  []model.RepoRef
	writes []model.RepoRef
	err    error
}

func (s stubScopeLookup) GitScope(context.Context, string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error) {
	return s.target, s.reads, s.writes, s.err
}

func TestAuthorizeAllowsFetchAndPushAgainstTheTarget(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "widgets"}}}
	for _, action := range []string{"info/refs", "git-upload-pack", "git-receive-pack"} {
		ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "widgets", action)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("action %q against the target should be allowed", action)
		}
	}
}

func TestAuthorizeAllowsFetchButNotPushAgainstAReadRepo(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{
		target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		reads:  []model.RepoRef{{Owner: "acme", Name: "shared-lib"}},
	}}
	ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "shared-lib", "git-upload-pack")
	if err != nil || !ok {
		t.Fatalf("fetch of a read repo should be allowed: ok=%v err=%v", ok, err)
	}
	ok, err = a.Authorize(context.Background(), "sandbox-0", "acme", "shared-lib", "git-receive-pack")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("push to a read-only repo should be denied")
	}
}

func TestAuthorizeDeniesAnUnrelatedRepo(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "widgets"}}}
	ok, err := a.Authorize(context.Background(), "sandbox-0", "someone-else", "repo", "git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("an unrelated repo should be denied")
	}
}

func TestAuthorizeDeniesEverythingWithNoLiveRun(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{}}
	ok, err := a.Authorize(context.Background(), "idle-sandbox", "acme", "widgets", "info/refs")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a sandbox with no live run should authorize nothing")
	}
}

func TestAuthorizeIsCaseAndDotGitInsensitive(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{target: &model.RepoRef{Owner: "Acme", Name: "Widgets"}}}
	ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "widgets.git", "git-upload-pack")
	if err != nil || !ok {
		t.Fatalf("expected a case/suffix-insensitive match: ok=%v err=%v", ok, err)
	}
}

func TestAuthorizeAllowsFetchAndPushAgainstWriteRepo(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{
		target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		writes: []model.RepoRef{{Owner: "acme", Name: "deploy-repo"}},
	}}
	for _, action := range []string{"info/refs", "git-upload-pack", "git-receive-pack"} {
		ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "deploy-repo", action)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("action %q against a granted write target should be allowed", action)
		}
	}
}

func TestAuthorizeDirectorWithoutTargetAllowsGrantedReadsAndWrites(t *testing.T) {
	a := &ModelAuthorizer{Store: stubScopeLookup{
		target: nil, // Director has no fixed target
		reads:  []model.RepoRef{{Owner: "acme", Name: "state-repo"}, {Owner: "acme", Name: "read-repo"}},
		writes: []model.RepoRef{{Owner: "acme", Name: "write-repo"}},
	}}
	// Reads allow fetch/clone, deny push
	ok, err := a.Authorize(context.Background(), "director-sandbox", "acme", "read-repo", "git-upload-pack")
	if err != nil || !ok {
		t.Fatalf("fetch of read repo should be allowed: ok=%v err=%v", ok, err)
	}
	ok, err = a.Authorize(context.Background(), "director-sandbox", "acme", "read-repo", "git-receive-pack")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("push to read-only repo should be denied")
	}

	// Writes allow fetch/clone and push
	for _, action := range []string{"info/refs", "git-upload-pack", "git-receive-pack"} {
		ok, err := a.Authorize(context.Background(), "director-sandbox", "acme", "write-repo", action)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("action %q against write repo should be allowed for director", action)
		}
	}

	// Unrelated repo denied
	ok, err = a.Authorize(context.Background(), "director-sandbox", "acme", "other-repo", "git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("unrelated repo should be denied")
	}
}

func TestAuthorizePropagatesAStoreError(t *testing.T) {
	boom := errors.New("boom")
	a := &ModelAuthorizer{Store: stubScopeLookup{err: boom}}
	if _, err := a.Authorize(context.Background(), "sandbox-0", "acme", "widgets", "info/refs"); !errors.Is(err, boom) {
		t.Errorf("expected the store error to propagate, got %v", err)
	}
}
