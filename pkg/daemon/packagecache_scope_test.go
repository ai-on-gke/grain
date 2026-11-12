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

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

type stubGitScopes struct {
	scopes map[string]struct {
		target        *model.RepoRef
		reads, writes []model.RepoRef
	}
	targetRepos []string
}

func (s stubGitScopes) GitScope(_ context.Context, sandbox string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error) {
	if sandbox == "run-broken" {
		return nil, nil, nil, errors.New("database is locked")
	}
	sc := s.scopes[sandbox]
	return sc.target, sc.reads, sc.writes, nil
}

func (s stubGitScopes) TargetRepos(_ context.Context) ([]string, error) {
	return s.targetRepos, nil
}

func TestPackageCacheRepoScopeMatchesTheGitProxyGrants(t *testing.T) {
	clients := &pkgcache.ClientNames{}
	clients.RegisterClient(net.ParseIP("10.40.0.2"), "run-target")
	clients.RegisterClient(net.ParseIP("10.40.0.3"), "run-reads")
	clients.RegisterClient(net.ParseIP("10.40.0.4"), "run-no-repo")
	clients.RegisterClient(net.ParseIP("10.40.0.5"), "ci-job")
	clients.RegisterClient(net.ParseIP("10.40.0.6"), "run-broken")
	clients.RegisterClient(net.ParseIP("10.40.0.7"), "run-other-host")
	clients.RegisterClient(net.ParseIP("10.40.0.8"), "ci-write-job")
	clients.RegisterClient(net.ParseIP("10.40.0.9"), "run-removed-target")
	clients.RegisterClient(net.ParseIP("10.40.0.10"), "run-forbidden")

	store := stubGitScopes{
		scopes: map[string]struct {
			target        *model.RepoRef
			reads, writes []model.RepoRef
		}{
			"run-target": {target: &model.RepoRef{Owner: "Acme", Name: "Private-Service"}},
			"run-reads": {
				target: &model.RepoRef{Owner: "acme", Name: "public"},
				reads:  []model.RepoRef{{Owner: "acme", Name: "lib"}},
				writes: []model.RepoRef{{Owner: "acme", Name: "deploy"}},
			},
			"run-no-repo":        {},
			"run-other-host":     {target: &model.RepoRef{Owner: "acme", Name: "private-service", Host: "git.example.com"}},
			"run-removed-target": {target: &model.RepoRef{Owner: "acme", Name: "retired"}},
			"run-forbidden":      {target: &model.RepoRef{Owner: "acme", Name: "secrets-state"}},
		},
		targetRepos: []string{"acme/private-service", "acme/public", "acme/deploy", "acme/secrets-state"},
	}
	ci := gitproxy.NewReadScopes()
	ci.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "ci-only"})
	ci.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "secrets-state"})

	ciWrite := gitproxy.NewWriteScopes()
	ciWrite.Grant("ci-write-job", model.RepoRef{Owner: "acme", Name: "ci-writable"})

	forbidden := gitproxy.NewForbiddenSet()
	forbidden.Set([]model.RepoRef{{Owner: "acme", Name: "secrets-state"}})

	scope := newPackageCacheRepoScope(clients, store, ci, ciWrite, forbidden)
	for _, tc := range []struct {
		from, owner, repo string
		want              bool
	}{
		{"10.40.0.2", "acme", "private-service", true},
		{"10.40.0.2", "acme", "other", false},
		{"10.40.0.3", "acme", "public", true},
		{"10.40.0.3", "acme", "lib", true},
		{"10.40.0.3", "acme", "deploy", true},
		{"10.40.0.3", "acme", "private-service", false},
		{"10.40.0.4", "acme", "private-service", false},
		{"10.40.0.5", "acme", "ci-only", true},
		{"10.40.0.5", "acme", "secrets-state", false},
		{"10.40.0.5", "acme", "private-service", false},
		{"10.40.0.6", "acme", "private-service", false},
		{"10.40.0.7", "acme", "private-service", false},
		{"10.40.0.7", "git.example.com/acme", "private-service", false},
		{"10.40.0.8", "acme", "ci-writable", true},
		{"10.40.0.8", "acme", "private-service", false},
		{"10.40.0.9", "acme", "retired", false},
		{"10.40.0.10", "acme", "secrets-state", false},
		{"10.40.0.99", "acme", "private-service", false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/token", nil)
		req.RemoteAddr = tc.from + ":443"
		if got := scope.MayReadRepo(req, tc.owner, tc.repo); got != tc.want {
			t.Errorf("sandbox at %s reading %s/%s = %v, want %v", tc.from, tc.owner, tc.repo, got, tc.want)
		}
	}

	if newPackageCacheRepoScope(nil, store, ci, ciWrite, forbidden) != nil {
		t.Error("a scope with no address registry should be nil, spending the credential for nobody")
	}
	nilStoreScope := newPackageCacheRepoScope(clients, nil, ci, ciWrite, forbidden)
	req := httptest.NewRequest(http.MethodGet, "/token", nil)
	req.RemoteAddr = "10.40.0.2:443"
	if nilStoreScope.MayReadRepo(req, "acme", "private-service") {
		t.Error("a scope with nil store should deny task sandboxes without panicking")
	}
}
