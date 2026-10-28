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

// The sandboxes with no network (model.NetworkNone), as both halves of
// this service see them.
//
// Such a sandbox reaches this service on purpose -- it is how its package
// installs work at all (cache.go) -- and so whatever this service will do
// for it is the whole of what NetworkNone means in practice. The resolver
// answers it NXDOMAIN for anything outside its allowlist (dns.go). The
// HTTP half lets it read through the cache and nothing else (ServeHTTP's
// restrictedRefusal): GET and HEAD, and the POST git's own fetch is made
// of, but never a push, an upload, a DELETE, or a tunnel to a host that
// takes one. Every write path here is a write to the internet with a
// credential the sandbox supplies itself, and a prompt-injected run is
// handed exactly such a credential by whatever it was made to read.

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// ClientSet is the set of sandbox addresses with no network. The zero
// value is an empty set ready to use, and a nil *ClientSet is one that is
// always empty.
//
// One set is shared by the resolver (DNSConfig.Restricted) and the HTTP
// service (Config.Restricted), so a sandbox backend names a sandbox once
// and both halves hold it to the same thing. Its two mutators are the
// ones orchestrator.DNSEgressPolicy asks for, so the set is what a
// backend is handed.
//
// Calls nest: an address restricted twice stays restricted until it has
// been released twice, so a stale release for a sandbox whose address has
// since been reused cannot open the cache back up under the new one.
type ClientSet struct {
	mu     sync.RWMutex
	counts map[string]int
}

// RestrictClient adds ip to the set.
func (c *ClientSet) RestrictClient(ip net.IP) {
	if c == nil || ip == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[clientKey(ip)]++
}

// UnrestrictClient releases one RestrictClient for ip.
func (c *ClientSet) UnrestrictClient(ip net.IP) {
	if c == nil || ip == nil {
		return
	}
	key := clientKey(ip)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[key] <= 1 {
		delete(c.counts, key)
	} else {
		c.counts[key]--
	}
}

// Restricted reports whether ip is in the set.
func (c *ClientSet) Restricted(ip net.IP) bool {
	if c == nil || ip == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.counts[clientKey(ip)] > 0
}

// clientKey is ip in the one form the set stores it in, so that an IPv4
// address seen as a 16-byte IPv4-in-IPv6 slice is the same entry as the
// four-byte one.
func clientKey(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.String()
}

// restrictedClient reports whether r came from a sandbox with no
// network: whether the address it arrived from is in s.restricted, or its
// job token is marked ReadOnly.
func (s *Server) restrictedClient(r *http.Request) bool {
	if s.jobTokens != nil {
		if access, ok := s.jobTokens.Lookup(r); ok && access.ReadOnly {
			return true
		}
	}
	if s.restricted == nil {
		return false
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return s.restricted.Restricted(net.ParseIP(strings.TrimSpace(host)))
}

// restrictedRefusal is why r, from a sandbox with no network, is not
// served, or "" when it may be. It is the whole of the difference between
// such a sandbox and any other here: it may read through this cache --
// GET and HEAD anywhere the routes below would take them, and POST to
// git-upload-pack, which is how git's fetch says what it wants -- and it
// may do nothing else. A CONNECT is held to restrictedConnectHosts
// instead (serveCONNECT), since a tunnel's method says nothing about what
// travels inside it.
func restrictedRefusal(r *http.Request) string {
	const prefix = "grain's package cache serves a sandbox with no network reads only -- "
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if service := r.URL.Query().Get("service"); service != "" && service != "git-upload-pack" {
			// The ref advertisement that opens a push. Refused here
			// rather than at the push itself, so git says so before it
			// has packed anything.
			return prefix + fmt.Sprintf("a download, a git fetch -- and %s is the start of a git push, which goes "+
				"through grain's git proxy to this task's own repository or nowhere.", service)
		}
		return ""
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			return ""
		}
		if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			return prefix + "a download, a git fetch -- and this is a git push, which goes through grain's git proxy " +
				"to this task's own repository or nowhere."
		}
	}
	return prefix + fmt.Sprintf("GET, HEAD, and a git fetch's POST to git-upload-pack -- and not %s %s. A push, an "+
		"upload or a delete through this cache would be a way out to the internet for a sandbox that was promised it "+
		"had none.", r.Method, r.URL.Path)
}

// restrictedConnectHosts is the part of connectHosts a sandbox with no
// network may open a tunnel to: the hosts that only ever serve downloads,
// named exactly. A tunnel is opaque, so the host is the only gate there
// is, and two kinds of host are left out of it:
//
//   - every wildcard (".debian.org", ".pkg.dev"), which names hosts
//     nobody here has looked at -- salsa.debian.org is a GitLab, and any
//     Artifact Registry project takes a push as readily as a pull;
//   - every host that takes a write at all: the container registries, the
//     npm registry (npm publish is a PUT to it), and github.com itself,
//     which serves git-receive-pack. (PyPI takes uploads at
//     upload.pypi.org, which is not a host this service fetches from.)
//
// A sandbox with no network loses nothing it was meant to have: grain
// points its apt, pip, npm, go and docker at this service's plain-HTTP
// routes, and transparent interception -- which is not a tunnel, and
// is held to restrictedRefusal -- covers the rest.
func (s *Server) restrictedConnectHosts() []string {
	var hosts []string
	hosts = append(hosts, s.aptHosts...)
	hosts = append(hosts, s.pypiFileHosts()...)
	hosts = append(hosts, s.goSumDBHosts()...)
	hosts = append(hosts, s.githubFileHosts()...)
	hosts = append(hosts, s.playwrightFileHosts()...)
	hosts = append(hosts, hostOnly(s.goProxy()), hostOnly(s.playwrightURL()))

	writable := map[string]bool{
		hostKey(hostOnly(s.githubURL())): true,
		"github.com":                     true,
		"gist.github.com":                true,
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.HasPrefix(host, ".") || writable[hostKey(host)] || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}
