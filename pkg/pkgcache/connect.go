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

// The CONNECT half: what this service does when something points a
// blanket proxy at it and then fetches an https:// URL.
//
// Nothing grain writes into a sandbox does that. apt is given a proxy per
// archive host with https for the same host sent DIRECT, pip an index
// URL, npm and go a registry each, and a container the per-ecosystem
// settings in ChildContainerEnv and no proxy at all (sandbox.go). But
// plenty of things a run does for itself do: a `docker build --build-arg
// HTTP_PROXY=...`, a script that exports http_proxy for everything it
// runs, a Dockerfile whose sources.list names an archive over TLS,
// `debootstrap --arch=amd64 trixie /build/root https://deb.debian.org/debian`.
// An HTTP proxy reached for an https:// URL is reached by CONNECT, and
// this service answered every CONNECT with 405 until 2026-09-18. That is
// not a fallback to a direct fetch; it is a hard failure:
//
//	E: Failed to fetch https://deb.debian.org/debian/pool/main/c/cpio/cpio_2.13.deb
//	   Invalid response from proxy: HTTP/1.1 405 Method Not Allowed
//
// and the build stops. It took out every image build in this deployment,
// on every branch, for three days, and cost a repository downstream of it
// the same hunt (kontur#26).
//
// So a CONNECT naming a host this service would have fetched from anyway
// is tunnelled straight through to that host, uncached. A tunnel is
// opaque -- there is nothing in it this service could cache, and no way
// for it to see what travels over it -- so this is not a feature of the
// cache but a way of not breaking a build: the fetch is as slow as it
// would be with no cache at all, and it works. The sandbox's own apt is
// still pointed the other way (https DIRECT, where the transparent
// interception in dns.go and ca.go caches it properly); this is for the
// clients grain cannot configure.
//
// The allowlist is the part that matters, and it is the same one the
// absolute-form GET path enforces: every host the ecosystem halves of
// this service will fetch from, and port 443 only. A sandbox running with
// model.NetworkNone has had its default route taken away precisely so
// that this deployment's own services are all it can reach, and a tunnel
// is opaque -- one to a registry or to github.com carries a push as
// easily as a pull -- so such a sandbox is held to a narrower list,
// restrictedConnectHosts: the exact hosts that serve nothing but
// downloads, which adds a TLS connection to what it already had in
// plaintext and nothing more (restrict.go). A CONNECT anywhere else is
// refused with 403 and a sentence, exactly as an absolute-form GET for
// the same host is -- and 403 rather than 405 because the status line is
// the whole of what a refused CONNECT tells its client, so it had better
// be the status that means "not this host" rather than the one that means
// "not this method".

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// connectPort is the only port a tunnel is opened to. Every archive,
// index and registry this service fetches from serves TLS on 443; a
// CONNECT to any other port is not a package download, and this service
// is not the way there.
const connectPort = "443"

// connectDialTimeout is how long the tunnel's own dial may take. What
// travels over it afterwards is not timed here at all -- a few hundred
// megabytes of .deb over a slow link is an ordinary thing for one of
// these to carry -- and the client gives up for itself when it wants to.
const connectDialTimeout = 30 * time.Second

// serveCONNECT answers one CONNECT: a tunnel to an allowlisted host on
// 443, or a refusal that says why there is none.
func (s *Server) serveCONNECT(w http.ResponseWriter, r *http.Request, restricted bool) {
	target := strings.TrimSpace(r.Host)
	if target == "" {
		target = strings.TrimSpace(r.URL.Host)
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		host, port = target, connectPort
	}
	if host == "" {
		s.refuse(w, r, http.StatusBadRequest,
			"grain's package cache was asked to open a tunnel to nowhere: a CONNECT names the host and port it wants.")
		return
	}
	if port != connectPort {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache tunnels https to the archives and registries it serves on port %s and nothing else, "+
				"so it will not open a tunnel to %s. It is a package cache, not a general-purpose proxy.",
			connectPort, target))
		return
	}
	if restricted {
		if allowed := s.restrictedConnectHosts(); !hostAllowed(host, allowed) {
			s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
				"grain's package cache will not tunnel to %s for this sandbox: it has no network of its own, and a "+
					"tunnel is opaque, so it is only opened to hosts that serve nothing but downloads (%s). A registry, "+
					"github.com or a whole domain could carry a push as easily as a pull. Fetch through the cache's "+
					"own routes, which grain configures apt, pip, npm, go and docker for, or give this task network access.",
				host, strings.Join(allowed, ", ")))
			return
		}
	}
	allowed := s.connectHosts()
	if !hostAllowed(host, allowed) {
		s.refuse(w, r, http.StatusForbidden, fmt.Sprintf(
			"grain's package cache does not fetch from %s, so it will not tunnel to it either: it serves distribution "+
				"archives, package indexes and container registries (%s) and nothing else. Something has pointed a "+
				"blanket http_proxy or https_proxy at this cache -- it is not a proxy for the whole internet. Fetch "+
				"this host directly, or turn this cache off in Settings -> Sandbox.",
			host, strings.Join(allowed, ", ")))
		return
	}

	// Everything that can fail happens before the connection is taken
	// over: an archive this service cannot reach is then a 502 with a
	// sentence in it, rather than a hijacked connection that is dropped
	// without a word.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.refuse(w, r, http.StatusInternalServerError,
			"grain's package cache cannot tunnel over this connection: whatever is carrying this request will not hand "+
				"a handler the connection itself, which is what a tunnel is made of.")
		return
	}
	upstream, err := s.dial(r.Context(), net.JoinHostPort(host, connectPort))
	if err != nil {
		s.logf("package cache: opening a tunnel to %s: %v", target, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "grain's package cache could not reach %s: %v\n", target, err)
		return
	}
	defer upstream.Close()

	client, buf, err := hijacker.Hijack()
	if err != nil {
		s.logf("package cache: taking over the connection for a tunnel to %s: %v", target, err)
		return
	}
	defer client.Close()

	// One line per tunnel, unlike a successful fetch, which is not logged
	// at all: a tunnel is a download this cache is deliberately not
	// keeping, and an operator asking why an https archive never fills the
	// cache has nowhere else to read that from.
	s.logf("package cache: tunnelling to %s, uncached -- a CONNECT is opaque, so nothing in it can be stored", target)

	if _, err := buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buf.Flush(); err != nil {
		return
	}
	tunnel(client, buf, upstream)
}

// tunnel copies bytes both ways until each direction has ended.
//
// The client side is read through the buffer the hijack came with rather
// than from the connection: a client is entitled to have sent its first
// TLS bytes along with the CONNECT, and if it did they are sitting in
// there and not on the socket.
//
// Each direction is closed on its own when it ends, rather than both
// being closed when the first one does, so that a client which has
// finished sending can still read the rest of what it asked for.
func tunnel(client net.Conn, buf *bufio.ReadWriter, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(upstream, buf)
		halfClose(upstream)
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
		halfClose(client)
	}()
	wg.Wait()
}

// halfClose ends one direction of a connection, and the whole of it when
// that is all this connection can do.
func halfClose(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// dial opens the one connection a tunnel is made of, through whatever
// dialer Config.Client's transport carries. The same route out as a
// fetch takes, which is what a deployment with an egress of its own
// configures once and what the tests point at a loopback archive.
func (s *Server) dial(ctx context.Context, addr string) (net.Conn, error) {
	if tr, ok := s.client.Transport.(*http.Transport); ok && tr.DialContext != nil {
		return tr.DialContext(ctx, "tcp", addr)
	}
	d := net.Dialer{Timeout: connectDialTimeout}
	return d.DialContext(ctx, "tcp", addr)
}

// connectHosts is every host this service will open a tunnel to: the
// union of what each ecosystem half of it fetches from, which is exactly
// the set an absolute-form GET or a transparent request is held to.
//
// Sorted and deduplicated, because it is also the list the refusal above
// prints, and a list that reordered itself between two requests would
// make two identical failures look like different ones.
func (s *Server) connectHosts() []string {
	hosts := []string{"docker.io", "auth.docker.io", "registry.yarnpkg.com"}
	hosts = append(hosts, s.aptHosts...)
	hosts = append(hosts, s.pypiFileHosts()...)
	hosts = append(hosts, s.goSumDBHosts()...)
	hosts = append(hosts, s.githubFileHosts()...)
	hosts = append(hosts, s.playwrightFileHosts()...)
	hosts = append(hosts, DefaultOCIRegistries...)
	for _, endpoint := range []string{
		s.pypiIndex(), s.npmRegistry(), s.goProxy(),
		s.dockerRegistry(), s.ghcrRegistry(), s.gcrRegistry(),
		s.githubURL(), s.playwrightURL(),
	} {
		hosts = append(hosts, hostOnly(endpoint))
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}
