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

// The GitHub half: serving both transparent requests for github.com,
// codeload.github.com, raw.githubusercontent.com, and objects.githubusercontent.com
// (intercepted via DNS) and legacy /github/ paths.
//
// What is stored:
// - Git Smart HTTP upload-pack result streams (packfiles): Git objects and packfiles
//   requested by SHA (in `want <SHA>` negotiation) are content-addressed and immutable.
//   They are cached keyed by URL and request body digest.
// - Release assets (/releases/download/...) and tag/commit archives (/archive/refs/tags/...,
//   /archive/<commit-sha>..., codeload archives): immutable release and archive artifacts.
// - Raw file downloads addressed by commit SHA (raw.githubusercontent.com/<owner>/<repo>/<commit-sha>/...).
//
// What is forwarded fresh every time (never stored):
// - Git reference advertisements (/info/refs?service=git-upload-pack): these change
//   whenever commits, branches, or tags are updated upstream. Forwarding them fresh
//   ensures that fetches and clones always discover current refs.
// - Branch archives (e.g. /archive/refs/heads/..., /archive/main.tar.gz) and mutable raw paths.
// - Git push operations (/git-receive-pack).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// githubPrefix is the legacy path prefix for github.
const githubPrefix = "/github/"

// DefaultGitHubURL is the upstream GitHub base URL when none is configured.
const DefaultGitHubURL = "https://github.com"

// DefaultGitHubFileHosts are the GitHub hosts intercepted and served:
// github.com for git and the release and archive URLs, and the hosts
// those redirect to for the bytes. Named one by one rather than as
// ".github.com" and ".githubusercontent.com", which also take in
// api.github.com, uploads.github.com and gist.githubusercontent.com --
// GitHub's API and a place anyone can write to, none of which is a
// download this cache could keep, and all of which an open CONNECT
// tunnel (connect.go) would hand to whoever asked.
var DefaultGitHubFileHosts = []string{
	"github.com",
	"codeload.github.com",
	"raw.githubusercontent.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

// maxGitRequestBodyBytes bounds a git upload-pack request body read into memory.
const maxGitRequestBodyBytes = 32 << 20

// githubMethodAllowed reports whether r is a request this cache forwards
// to GitHub at all: GET and HEAD anywhere, which is every archive, raw
// file, release asset and ref advertisement, and POST only to git's own
// smart-HTTP endpoints, which is how a fetch and a push say what they
// want. Everything else -- a POST to api.github.com, a PUT, a DELETE --
// is GitHub's API rather than a download, and forwarding it would make
// this cache a way for a sandbox with no network to call that API with
// whatever credential it can put in an Authorization header, the one
// thing every other ecosystem here already refuses (ServeHTTP).
//
// host is the intercepted host a transparent request named, or "" for
// the legacy /github/ route, which only ever reaches github.com. Git's
// POSTs are forwarded only to the hosts that serve git (gitHosts): the
// path alone would let a POST to api.github.com/<anything>/git-upload-pack
// through, since ".github.com" intercepts the API host too.
func githubMethodAllowed(r *http.Request, host string) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
		if host != "" && !gitHosts[canonicalGitHubHost(host)] {
			return false
		}
		return strings.HasSuffix(r.URL.Path, "/git-upload-pack") || strings.HasSuffix(r.URL.Path, "/git-receive-pack")
	}
	return false
}

// gitHosts are the intercepted GitHub hosts that serve git's smart-HTTP
// protocol. Not api.github.com or uploads.github.com, which are GitHub's
// API whatever the path says, should a deployment's GitHubHosts name
// them.
var gitHosts = map[string]bool{"github.com": true}

// canonicalGitHubHost lowercases host and drops any port and trailing dot.
func canonicalGitHubHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

// refuseGitHubMethod is githubMethodAllowed's refusal.
func (s *Server) refuseGitHubMethod(w http.ResponseWriter, r *http.Request) {
	s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: for GitHub it "+
		"answers GET and HEAD, and POST to git-upload-pack and git-receive-pack, not %s %s.", r.Method, r.URL.Path))
}

// serveGitHub answers one legacy request against this service's path /github/.
func (s *Server) serveGitHub(w http.ResponseWriter, r *http.Request) {
	if !githubMethodAllowed(r, "") {
		s.refuseGitHubMethod(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.EscapedPath(), strings.TrimSuffix(githubPrefix, "/"))
	if strings.Contains(rest, "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	upstream := strings.TrimSuffix(s.githubURL(), "/") + rest
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	cleanPath := r.URL.Path
	switch {
	case strings.HasSuffix(cleanPath, "/info/refs"):
		// Ref discovery is mutable: always forward fresh
		s.serve(w, r, object{kind: KindGitHub, url: upstream, cacheable: false})
	case strings.HasSuffix(cleanPath, "/git-upload-pack") && r.Method == http.MethodPost:
		s.serveGitUploadPack(w, r, upstream, strings.TrimSuffix(s.githubURL(), "/")+rest)
	case strings.HasSuffix(cleanPath, "/git-receive-pack"):
		// Push operation: pass through uncached
		s.serve(w, r, object{kind: KindGitHub, url: upstream, cacheable: false})
	default:
		s.serve(w, r, object{kind: KindGitHub, url: upstream, cacheable: githubCacheable("github.com", cleanPath)})
	}
}

// serveTransparentGitHub answers one transparent request against an intercepted GitHub domain.
func (s *Server) serveTransparentGitHub(w http.ResponseWriter, r *http.Request, host string) {
	gateHost := host
	if s.githubURLVal != "" && canonicalGitHubHost(host) == canonicalGitHubHost(hostOnly(s.githubURLVal)) {
		// The configured GitHub (a GHE host, or a test's stand-in) is
		// where the legacy route sends git too.
		gateHost = ""
	}
	if !githubMethodAllowed(r, gateHost) {
		s.refuseGitHubMethod(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}

	scheme := "https"
	if s.fileScheme != "" {
		scheme = s.fileScheme
	} else if r.TLS == nil {
		scheme = "http"
	}

	upstreamHost := host
	if s.githubURLVal != "" && (host == "github.com" || host == "") {
		if u, err := url.Parse(s.githubURL()); err == nil && u.Host != "" {
			upstreamHost = u.Host
			if u.Scheme != "" {
				scheme = u.Scheme
			}
		} else {
			upstreamHost = hostOnly(s.githubURL())
		}
	}

	upstream := scheme + "://" + upstreamHost + r.URL.RequestURI()
	cleanPath := r.URL.Path

	switch {
	case strings.HasSuffix(cleanPath, "/info/refs"):
		// Ref advertisements change on push; always query upstream for freshness
		s.serve(w, r, object{kind: KindGitHub, url: upstream, cacheable: false})
	case strings.HasSuffix(cleanPath, "/git-upload-pack") && r.Method == http.MethodPost:
		baseURL := scheme + "://" + upstreamHost + r.URL.Path
		s.serveGitUploadPack(w, r, upstream, baseURL)
	case strings.HasSuffix(cleanPath, "/git-receive-pack"):
		s.serve(w, r, object{kind: KindGitHub, url: upstream, cacheable: false})
	default:
		s.serve(w, r, object{kind: KindGitHub, url: upstream, cacheable: githubCacheable(host, cleanPath)})
	}
}

// serveGitUploadPack handles POST requests to git-upload-pack, caching packfile results
// keyed by the repository URL and SHA256 of the request negotiation body.
func (s *Server) serveGitUploadPack(w http.ResponseWriter, r *http.Request, upstreamURL, baseURL string) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxGitRequestBodyBytes))
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache could not read git request body: %v", err))
		return
	}

	sum := sha256.Sum256(bodyBytes)
	key := baseURL + "#body=" + hex.EncodeToString(sum[:])
	cacheable := isGitUploadPackCacheable(bodyBytes)
	obj := object{kind: KindGitHub, url: upstreamURL, key: key, cacheable: cacheable}

	if cacheable && s.serveFromCache(w, r, obj) {
		return
	}

	fKey := flightKey(obj)
	if cacheable {
		if wait, leader := s.fetches.begin(fKey); leader {
			defer s.fetches.end(fKey)
			if s.serveFromCache(w, r, obj) {
				return
			}
		} else {
			select {
			case <-wait:
			case <-r.Context().Done():
				return
			}
			if s.serveFromCache(w, r, obj) {
				return
			}
		}
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache cannot fetch %s: %v", upstreamURL, err))
		return
	}
	copyRequestHeaders(req, r)
	resp, err := s.client.Do(req)
	if err != nil {
		s.logf("package cache: fetching %s: %v", upstreamURL, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "grain's package cache could not reach %s: %v\n", upstreamURL, err)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	var entry ObjectWriter
	isPackResult := strings.Contains(resp.Header.Get("Content-Type"), "application/x-git-upload-pack-result")
	if cacheable && resp.StatusCode == http.StatusOK && isPackResult {
		if entry, err = s.cache.NewWriter(KindGitHub, key); err != nil {
			s.logf("package cache: not storing %s: %v", upstreamURL, err)
			entry = nil
		}
	}

	status := "miss"
	if entry == nil {
		status = "pass"
	}
	w.Header().Set(cacheStatusHeader, status)
	w.WriteHeader(resp.StatusCode)

	if r.Method == http.MethodHead {
		if entry != nil {
			entry.Abort()
		}
		return
	}

	_, complete := copyBody(w, entry, resp.Body, resp.ContentLength)
	if entry == nil {
		return
	}
	if entry.Oversize() {
		s.logf("package cache: %s is larger than the %dMB a single entry may be -- not caching it",
			upstreamURL, s.cache.MaxEntryBytes()/(1<<20))
		return
	}
	if !complete {
		entry.Abort()
		return
	}
	entry.Commit(Meta{URL: upstreamURL, ContentType: resp.Header.Get("Content-Type")})
}

// isGitUploadPackCacheable reports whether a git upload-pack request body asks for
// immutable object SHAs rather than mutable named refs.
func isGitUploadPackCacheable(bodyBytes []byte) bool {
	if bytes.Contains(bodyBytes, []byte("want-ref ")) {
		return false
	}
	return bytes.Contains(bodyBytes, []byte("want "))
}

// githubCacheable reports whether a GitHub GET request path names an immutable artifact.
func githubCacheable(host, rawPath string) bool {
	p := strings.ToLower(rawPath)
	if strings.Contains(p, "/info/refs") || strings.Contains(p, "/git-upload-pack") || strings.Contains(p, "/git-receive-pack") {
		return false
	}
	// Release downloads are immutable
	if strings.Contains(p, "/releases/download/") {
		return true
	}
	// Tag archive downloads are immutable
	if strings.Contains(p, "/archive/refs/tags/") {
		return true
	}
	// Branch archives are mutable
	if strings.Contains(p, "/archive/refs/heads/") {
		return false
	}
	// Direct archive /archive/<ref>.(tar.gz|zip|tgz)
	if strings.Contains(p, "/archive/") {
		ext := path.Ext(p)
		if ext == ".gz" || ext == ".zip" || ext == ".tgz" {
			base := path.Base(p)
			name := strings.TrimSuffix(base, ext)
			name = strings.TrimSuffix(name, ".tar")
			if isHexSHA(name) || isSemverish(name) {
				return true
			}
		}
		return false
	}
	// codeload.github.com archives
	if host == "codeload.github.com" {
		if strings.Contains(p, "/tar.gz/refs/tags/") || strings.Contains(p, "/zip/refs/tags/") {
			return true
		}
		if strings.Contains(p, "/tar.gz/refs/heads/") || strings.Contains(p, "/zip/refs/heads/") {
			return false
		}
		base := path.Base(p)
		if isHexSHA(base) || isSemverish(base) {
			return true
		}
		return false
	}
	// raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>
	if host == "raw.githubusercontent.com" {
		parts := strings.Split(strings.TrimPrefix(rawPath, "/"), "/")
		if len(parts) >= 3 {
			ref := parts[2]
			if isHexSHA(ref) {
				return true
			}
		}
		return false
	}
	return false
}

// isHexSHA reports whether s is a 40-character (SHA-1) or 64-character (SHA-256) hex string.
func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func (s *Server) githubURL() string {
	if s.githubURLVal != "" {
		return s.githubURLVal
	}
	return DefaultGitHubURL
}

func (s *Server) githubFileHosts() []string {
	if len(s.githubHosts) > 0 {
		return s.githubHosts
	}
	return DefaultGitHubFileHosts
}
