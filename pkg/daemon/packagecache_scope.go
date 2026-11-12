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
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// packageCacheRepoScope is the package cache's pkgcache.RepoScope: which
// repositories the sandbox a request came from may read, and so which
// ones the cache may spend this deployment's GitHub credential on for it.
//
// It answers with the same authorizer chain the git proxy uses
// (gitproxy.ReadOnlyScopeAuthorizer over gitproxy.ModelAuthorizer): a
// sandbox may read its live run's target and its task's reads and writes
// (subject to TargetRepos removal), or a CI job's read and write grants,
// and never a repository in the deployment's ForbiddenSet. The difference
// is how the sandbox is found. A request to the package cache is
// intercepted by DNS and carries no sandbox token, so it is resolved by
// the address it arrived from, which the sandbox backend registers in
// clients as it hands each sandbox over. An address nobody registered
// names no sandbox and reads nothing.
type packageCacheRepoScope struct {
	clients    *pkgcache.ClientNames
	authorizer gitproxy.Authorizer
}

type denyAllAuthorizer struct{}

func (denyAllAuthorizer) Authorize(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

// newPackageCacheRepoScope returns the scope, or nil when there is no
// registry of sandbox addresses to resolve a request by -- a nil
// RepoScope, which the cache reads as "spend the credential for nobody".
func newPackageCacheRepoScope(
	clients *pkgcache.ClientNames,
	store gitproxy.ScopeLookup,
	ci *gitproxy.ReadScopes,
	ciWrite *gitproxy.WriteScopes,
	forbidden *gitproxy.ForbiddenSet,
) pkgcache.RepoScope {
	if clients == nil {
		return nil
	}
	var next gitproxy.Authorizer = denyAllAuthorizer{}
	if store != nil {
		next = &gitproxy.ModelAuthorizer{Store: store, Forbidden: forbidden}
	}
	return &packageCacheRepoScope{
		clients: clients,
		authorizer: &gitproxy.ReadOnlyScopeAuthorizer{
			Scopes:      ci,
			WriteScopes: ciWrite,
			Next:        next,
			Forbidden:   forbidden,
		},
	}
}

// MayReadRepo implements pkgcache.RepoScope.
func (p *packageCacheRepoScope) MayReadRepo(r *http.Request, owner, repo string) bool {
	// GHCR packages belong to GitHub repositories on the default git host;
	// rejecting '/' in owner ensures a caller can never form a "<host>/<owner>"
	// ScopeOwner that would match a grant on a non-default git host.
	if owner == "" || repo == "" || strings.Contains(owner, "/") {
		return false
	}
	sandbox, ok := p.clients.NameFor(r)
	if !ok || sandbox == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	allowed, err := p.authorizer.Authorize(ctx, sandbox, owner, repo, "git-upload-pack")
	if err != nil {
		log.Printf("grain daemon: package cache: resolving the repository scope of sandbox %s: %v -- "+
			"not spending this deployment's GitHub credential for it", sandbox, err)
		return false
	}
	return allowed
}
