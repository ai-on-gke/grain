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

package pkgcache

// Whose repositories a request may spend the deployment's credential on.
//
// Every sandbox reaches this service, and the deployment's GitHub
// credential (Config.Credentials) can read every private package under
// every owner it is connected to. Attaching it to whatever a sandbox asks
// for would hand each sandbox -- a task with no repository at all, or one
// scoped to a single public repository -- a pull token for all of them.
// So the credential is only ever spent for a repository the calling
// sandbox could already clone through the git proxy: its run's target,
// its task's reads and writes, or a CI job's granted repository. Config.
// RepoScope is what answers that; with none, the credential is spent for
// nobody, which is the fail-closed default a missing answer has to have.
//
// A GHCR image is taken to belong to the GitHub repository it is named
// after (ghcr.io/<owner>/<repo>[/<sub>]), which is the same convention
// RegistryCredentials.PackageToken already selects a credential by.

import (
	"net"
	"net/http"
	"strings"
	"sync"
)

// RepoScope answers whether the sandbox r came from may read the GitHub
// repository owner/repo -- and so whether this service may spend the
// deployment's own credential on that repository on its behalf.
type RepoScope interface {
	MayReadRepo(r *http.Request, owner, repo string) bool
}

// RepoScopeFunc adapts a function to RepoScope.
type RepoScopeFunc func(r *http.Request, owner, repo string) bool

// MayReadRepo implements RepoScope.
func (f RepoScopeFunc) MayReadRepo(r *http.Request, owner, repo string) bool {
	return f(r, owner, repo)
}

// ClientNames maps the address a sandbox's requests arrive from to the
// sandbox's name, so that a request intercepted by DNS -- which carries
// nothing else that says who sent it -- can be resolved to the sandbox,
// and from there to that sandbox's repository scope. The zero value is
// an empty registry ready to use, and a nil *ClientNames is one that
// never names anybody.
//
// A sandbox backend registers each sandbox's addresses as it hands the
// sandbox over and releases them as it tears it down (the same
// lifecycle DNSEgressPolicy follows for ClientSet). Release names the
// sandbox as well as the address, so a stale release for an address
// that has since been given to another sandbox leaves the new
// registration alone.
type ClientNames struct {
	mu    sync.RWMutex
	names map[string]string
}

// RegisterClient records that requests from ip come from sandbox.
func (c *ClientNames) RegisterClient(ip net.IP, sandbox string) {
	if c == nil || len(ip) == 0 || ip.IsUnspecified() || sandbox == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names == nil {
		c.names = map[string]string{}
	}
	c.names[clientKey(ip)] = sandbox
}

// ReleaseClient forgets ip, if it still names sandbox.
func (c *ClientNames) ReleaseClient(ip net.IP, sandbox string) {
	if c == nil || ip == nil {
		return
	}
	key := clientKey(ip)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names[key] == sandbox {
		delete(c.names, key)
	}
}

// Name is the sandbox registered under ip, if any.
func (c *ClientNames) Name(ip net.IP) (string, bool) {
	if c == nil || ip == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	name, ok := c.names[clientKey(ip)]
	return name, ok
}

// NameFor is the sandbox registered under the address r arrived from.
func (c *ClientNames) NameFor(r *http.Request) (string, bool) {
	return c.Name(RequestClientIP(r))
}

// RequestClientIP is the address r arrived from -- its TCP peer, never a
// header the caller wrote.
func RequestClientIP(r *http.Request) net.IP {
	if r == nil {
		return nil
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return net.ParseIP(strings.TrimSpace(host))
}

// mayUseCredentials reports whether this service may spend the
// deployment's credential on owner/repo for the sandbox r came from.
func (s *Server) mayUseCredentials(r *http.Request, owner, repo string) bool {
	if s.repoScope == nil || r == nil || owner == "" || repo == "" {
		return false
	}
	return s.repoScope.MayReadRepo(r, owner, repo)
}

// ghcrRepoOf is the GitHub repository a GHCR image path belongs to: the
// owner, and the first path element after it (the image may have more).
func ghcrRepoOf(owner, repo string) (string, string) {
	if i := strings.Index(repo, "/"); i > 0 {
		repo = repo[:i]
	}
	return owner, repo
}
