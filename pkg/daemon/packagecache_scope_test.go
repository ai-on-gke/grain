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

type stubGitScopes map[string]struct {
	target        *model.RepoRef
	reads, writes []model.RepoRef
}

func (s stubGitScopes) GitScope(_ context.Context, sandbox string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error) {
	if sandbox == "run-broken" {
		return nil, nil, nil, errors.New("database is locked")
	}
	sc := s[sandbox]
	return sc.target, sc.reads, sc.writes, nil
}

func TestPackageCacheRepoScopeMatchesTheGitProxyGrants(t *testing.T) {
	clients := &pkgcache.ClientNames{}
	clients.RegisterClient(net.ParseIP("10.40.0.2"), "run-target")
	clients.RegisterClient(net.ParseIP("10.40.0.3"), "run-reads")
	clients.RegisterClient(net.ParseIP("10.40.0.4"), "run-no-repo")
	clients.RegisterClient(net.ParseIP("10.40.0.5"), "ci-job")
	clients.RegisterClient(net.ParseIP("10.40.0.6"), "run-broken")
	clients.RegisterClient(net.ParseIP("10.40.0.7"), "run-other-host")

	store := stubGitScopes{
		"run-target": {target: &model.RepoRef{Owner: "Acme", Name: "Private-Service"}},
		"run-reads": {
			target: &model.RepoRef{Owner: "acme", Name: "public"},
			reads:  []model.RepoRef{{Owner: "acme", Name: "lib"}},
			writes: []model.RepoRef{{Owner: "acme", Name: "deploy"}},
		},
		"run-no-repo":    {},
		"run-other-host": {target: &model.RepoRef{Owner: "acme", Name: "private-service", Host: "git.example.com"}},
	}
	ci := gitproxy.NewReadScopes()
	ci.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "ci-only"})

	scope := newPackageCacheRepoScope(clients, store, ci)
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
		{"10.40.0.5", "acme", "private-service", false},
		{"10.40.0.6", "acme", "private-service", false},
		{"10.40.0.7", "acme", "private-service", false},
		{"10.40.0.99", "acme", "private-service", false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/token", nil)
		req.RemoteAddr = tc.from + ":443"
		if got := scope.MayReadRepo(req, tc.owner, tc.repo); got != tc.want {
			t.Errorf("sandbox at %s reading %s/%s = %v, want %v", tc.from, tc.owner, tc.repo, got, tc.want)
		}
	}

	if newPackageCacheRepoScope(nil, store, ci) != nil {
		t.Error("a scope with no address registry should be nil, spending the credential for nobody")
	}
}
