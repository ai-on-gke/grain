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
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// packageCacheRepoScope is the package cache's pkgcache.RepoScope: which
// repositories the sandbox a request came from may read, and so which
// ones the cache may spend this deployment's GitHub credential on for it.
//
// It answers exactly what the git proxy would: a sandbox may read its
// live run's target, its task's reads and writes (model.Store.GitScope),
// and a CI job's granted repositories (gitproxy.ReadScopes). The
// difference is how the sandbox is found. A request to the package
// cache is intercepted by DNS and carries no sandbox token, so it is
// resolved by the address it arrived from, which the sandbox backend
// registers in clients as it hands each sandbox over. An address nobody
// registered names no sandbox and reads nothing.
type packageCacheRepoScope struct {
	clients *pkgcache.ClientNames
	store   gitproxy.ScopeLookup
	ci      *gitproxy.ReadScopes
}

// newPackageCacheRepoScope returns the scope, or nil when there is no
// registry of sandbox addresses to resolve a request by -- a nil
// RepoScope, which the cache reads as "spend the credential for nobody".
func newPackageCacheRepoScope(clients *pkgcache.ClientNames, store gitproxy.ScopeLookup, ci *gitproxy.ReadScopes) pkgcache.RepoScope {
	if clients == nil {
		return nil
	}
	return &packageCacheRepoScope{clients: clients, store: store, ci: ci}
}

// MayReadRepo implements pkgcache.RepoScope.
func (p *packageCacheRepoScope) MayReadRepo(r *http.Request, owner, repo string) bool {
	if owner == "" || repo == "" {
		return false
	}
	sandbox, ok := p.clients.NameFor(r)
	if !ok || sandbox == "" {
		return false
	}
	for _, granted := range p.ci.Repos(sandbox) {
		if sameDefaultHostRepo(granted, owner, repo) {
			return true
		}
	}
	if p.store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	target, reads, writes, err := p.store.GitScope(ctx, sandbox)
	if err != nil {
		log.Printf("grain daemon: package cache: resolving the repository scope of sandbox %s: %v -- "+
			"not spending this deployment's GitHub credential for it", sandbox, err)
		return false
	}
	if target != nil && sameDefaultHostRepo(*target, owner, repo) {
		return true
	}
	for _, list := range [][]model.RepoRef{reads, writes} {
		for _, granted := range list {
			if sameDefaultHostRepo(granted, owner, repo) {
				return true
			}
		}
	}
	return false
}

// sameDefaultHostRepo reports whether ref is owner/repo on the default git
// host. GHCR packages belong to GitHub repositories, so a grant for a
// repository on any other host is never a grant for one of them.
func sameDefaultHostRepo(ref model.RepoRef, owner, repo string) bool {
	if ref.Host != "" {
		return false
	}
	return strings.EqualFold(ref.Owner, owner) &&
		strings.EqualFold(strings.TrimSuffix(ref.Name, ".git"), strings.TrimSuffix(repo, ".git"))
}
